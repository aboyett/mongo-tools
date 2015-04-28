package archive

import (
	"bytes"
	"fmt"
	"github.com/mongodb/mongo-tools/common/intents"
	"github.com/mongodb/mongo-tools/common/log"
	"gopkg.in/mgo.v2/bson"
	"io"
	"path/filepath"
)

//Metadata implements intents.file
type Metadata struct {
	*bytes.Buffer
	Intent *intents.Intent
}

func (md *Metadata) Open() error {
	return nil
}
func (md *Metadata) Close() error {
	return nil
}

// DirLike represents the group of methods done on directories and files in dump directories,
// or in archives, when mongorestore is figuring out what intents to create
type DirLike interface {
	Name() string
	Path() string
	Size() int64
	IsDir() bool
	Stat() (DirLike, error)
	ReadDir() ([]DirLike, error)
	Parent() DirLike
}

// Prelude represents the knowledge gleaned from reading the prelude out of the archive
type Prelude struct {
	Header                 *Header
	DBS                    []string
	NamespaceMetadatas     []*CollectionMetadata
	NamespaceMetadatasByDB map[string][]*CollectionMetadata
}

// Read consumes and checks the magic number at the beginning of the archive,
// then it runs the parser with a Prelude as its consumer
func (prelude *Prelude) Read(in io.Reader) error {
	magicNumberBuf := make([]byte, 4)
	_, err := io.ReadAtLeast(in, magicNumberBuf, 4)
	if err != nil {
		return err
	}
	magicNumber := int32(
		(uint32(magicNumberBuf[0]) << 0) |
			(uint32(magicNumberBuf[1]) << 8) |
			(uint32(magicNumberBuf[2]) << 16) |
			(uint32(magicNumberBuf[3]) << 24),
	)

	if magicNumber != MagicNumber {
		return fmt.Errorf("stream or file does not apear to be a mongodump archive")
	}

	if prelude.NamespaceMetadatasByDB != nil {
		prelude.NamespaceMetadatasByDB = make(map[string][]*CollectionMetadata, 0)
	}

	parser := Parser{In: in}
	parserConsumer := &preludeParserConsumer{prelude: prelude}
	err = parser.ReadBlock(parserConsumer)
	if err != nil {
		return err
	}
	return nil
}

// NewPrelude generates a Prelude using the contents of an intent.Manager
func NewPrelude(manager *intents.Manager, maxProcs int) (*Prelude, error) {
	prelude := Prelude{
		Header: &Header{
			ArchiveFormatVersion:  archiveFormatVersion,
			ConcurrentCollections: int32(maxProcs),
		},
		NamespaceMetadatasByDB: make(map[string][]*CollectionMetadata, 0),
	}
	allIntents := manager.Intents()
	for _, intent := range allIntents {
		if intent.MetadataFile != nil {
			archiveMetadata, ok := intent.MetadataFile.(*Metadata)
			if !ok {
				return nil, fmt.Errorf("MetadataFile is not an archive.Metadata")
			}
			prelude.AddMetadata(&CollectionMetadata{
				Database:   intent.DB,
				Collection: intent.C,
				Metadata:   archiveMetadata.Buffer.String(),
			})
		} else {
			prelude.AddMetadata(&CollectionMetadata{
				Database:   intent.DB,
				Collection: intent.C,
			})
		}
	}
	return &prelude, nil
}

// AddMetadata adds a metadata data structure to a prelude and does the required bookkeeping
func (prelude *Prelude) AddMetadata(cm *CollectionMetadata) {
	prelude.NamespaceMetadatas = append(prelude.NamespaceMetadatas, cm)
	if prelude.NamespaceMetadatasByDB == nil {
		prelude.NamespaceMetadatasByDB = make(map[string][]*CollectionMetadata)
	}
	_, ok := prelude.NamespaceMetadatasByDB[cm.Database]
	if !ok {
		prelude.DBS = append(prelude.DBS, cm.Database)
	}
	prelude.NamespaceMetadatasByDB[cm.Database] = append(prelude.NamespaceMetadatasByDB[cm.Database], cm)
	log.Logf(log.Info, "archive prelude %v %v", cm.Database, cm.Collection)
}

func (prelude *Prelude) Write(out io.Writer) error {
	magicNumberBytes := make([]byte, 4)
	for i := range magicNumberBytes {
		magicNumberBytes[i] = byte(uint32(MagicNumber) >> uint(i*8))
	}
	_, err := out.Write(magicNumberBytes)
	if err != nil {
		return err
	}
	buf, err := bson.Marshal(prelude.Header)
	if err != nil {
		return err
	}
	_, err = out.Write(buf)
	if err != nil {
		return err
	}
	for _, cm := range prelude.NamespaceMetadatas {
		buf, err = bson.Marshal(cm)
		if err != nil {
			return err
		}
		_, err = out.Write(buf)
		if err != nil {
			return err
		}
	}
	_, err = out.Write(terminatorBytes)
	if err != nil {
		return err
	}
	return nil
}

// preludeParserConsumer wraps a Prelude, and implements ParserConsumer
type preludeParserConsumer struct {
	prelude *Prelude
}

// HeaderBSON is part of the ParserConsumer interface, it unmarshals archive Header's
func (hpc *preludeParserConsumer) HeaderBSON(data []byte) error {
	hpc.prelude.Header = &Header{}
	err := bson.Unmarshal(data, hpc.prelude.Header)
	if err != nil {
		return err
	}
	return nil
}

// BodyBSON is part of the ParserConsumer interface, it unmarshals CollectionMetadata's
func (hpc *preludeParserConsumer) BodyBSON(data []byte) error {
	cm := &CollectionMetadata{}
	err := bson.Unmarshal(data, cm)
	if err != nil {
		return err
	}
	hpc.prelude.AddMetadata(cm)
	return nil
}

// BodyBSON is part of the ParserConsumer interface
func (hpc *preludeParserConsumer) End() error {
	return nil
}

// PreludeExplorer implements DirLike
type PreludeExplorer struct {
	prelude    *Prelude
	database   string
	collection string
	isMetadata bool
}

// NewPreludeExplorer creates a PreludeExplorer from a Prelude
func (prelude *Prelude) NewPreludeExplorer() *PreludeExplorer {
	return &PreludeExplorer{
		prelude: prelude,
	}
}

// Name is part of the DirLike interface. It synthesizes a filename for the given "location" the prelude
func (pe *PreludeExplorer) Name() string {
	if pe.collection == "" {
		return pe.database
	}
	if pe.isMetadata {
		return pe.collection + ".metadata.json"
	}
	return pe.collection + ".bson"
}

// Path is part of the DirLike interface. It creates the full path for the "location" in the prelude
func (pe *PreludeExplorer) Path() string {
	if pe.collection == "" {
		return pe.database
	}
	if pe.database == "" {
		return pe.Name()
	}
	return filepath.Join(pe.database, pe.Name())
}

// Size is part of the DirLike interface. It returns the size from the metadata
// of the prelude, if the "location" is a collection
func (pe *PreludeExplorer) Size() int64 {
	if pe.IsDir() {
		return int64(0)
	}
	for _, ns := range pe.prelude.NamespaceMetadatas {
		if ns.Database == pe.database && ns.Collection == pe.collection {
			return int64(ns.Size)
		}
	}
	return int64(0)
}

// IsDir is part of the DirLike interface. All pe's that are not collections are Dir's
func (pe *PreludeExplorer) IsDir() bool {
	return pe.collection == ""
}

// Stat is part of the DirLike interface. os.Stat returns a FileInfo, and since
// DirLike is similar to FileInfo, we just return the pe, here.
func (pe *PreludeExplorer) Stat() (DirLike, error) {
	return pe, nil
}

// ReadDir is part of the DirLIke interface. ReadDir generates a list of PreludeExplorer's
// whose "locations" are encapsulated by the current pe's "location"
func (pe *PreludeExplorer) ReadDir() ([]DirLike, error) {
	if !pe.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}
	pes := []DirLike{}
	if pe.database == "" {
		topLevelNamespaceMetadatas, ok := pe.prelude.NamespaceMetadatasByDB[""]
		if ok {
			// basically for the oplog
			for _, topLevelNamespaceMetadata := range topLevelNamespaceMetadatas {
				pes = append(pes, &PreludeExplorer{
					prelude:    pe.prelude,
					collection: topLevelNamespaceMetadata.Collection,
				})
				if topLevelNamespaceMetadata.Metadata != "" {
					pes = append(pes, &PreludeExplorer{
						prelude:    pe.prelude,
						collection: topLevelNamespaceMetadata.Collection,
						isMetadata: true,
					})
				}
			}
		}
		for _, db := range pe.prelude.DBS {
			pes = append(pes, &PreludeExplorer{
				prelude:  pe.prelude,
				database: db,
			})
		}
	} else {
		namespaceMetadatas, ok := pe.prelude.NamespaceMetadatasByDB[pe.database]
		if !ok {
			return nil, fmt.Errorf("no such directory") //TODO: replace with real ERRNOs?
		}
		for _, namespaceMetadata := range namespaceMetadatas {
			pes = append(pes, &PreludeExplorer{
				prelude:    pe.prelude,
				database:   pe.database,
				collection: namespaceMetadata.Collection,
			})
			if namespaceMetadata.Metadata != "" {
				pes = append(pes, &PreludeExplorer{
					prelude:    pe.prelude,
					database:   pe.database,
					collection: namespaceMetadata.Collection,
					isMetadata: true,
				})
			}
		}
	}
	return pes, nil
}

// Parent implements the DirLike interface. It returns a pe without a collection, if there is one,
// otherwise, without a database
func (pe *PreludeExplorer) Parent() DirLike {
	if pe.collection != "" {
		return &PreludeExplorer{
			prelude:  pe.prelude,
			database: pe.database,
		}
	}
	return &PreludeExplorer{
		prelude: pe.prelude,
	}
}

// MetadataPreludeFile implements intents.file. It allows the metadata contained in the prelude to be opened and read
type MetadataPreludeFile struct {
	Intent  *intents.Intent
	Prelude *Prelude
	*bytes.Buffer
}

// Open is part of the intents.file interface, it finds the metadata in the prelude and creates a bytes.Buffer from it.
func (mpf *MetadataPreludeFile) Open() error {
	if mpf.Intent.C == "" {
		return fmt.Errorf("so such file") // what's the errno that occurs when one tries to open a directory
	}
	dbMetadatas, ok := mpf.Prelude.NamespaceMetadatasByDB[mpf.Intent.DB]
	if !ok {
		return fmt.Errorf("so such file") // what's the errno that occurs when one tries to open a directory
	}
	for _, metadata := range dbMetadatas {
		if metadata.Collection == mpf.Intent.C {
			mpf.Buffer = bytes.NewBufferString(metadata.Metadata)
			return nil
		}
	}
	return fmt.Errorf("so such file") // what's the errno that occurs when one tries to open a directory
}

// Close is part of the intents.file interface.
func (mpf *MetadataPreludeFile) Close() error {
	mpf.Buffer = nil
	return nil
}