package mongorestore

import (
	"github.com/mongodb/mongo-tools/common/db"
	"github.com/mongodb/mongo-tools/common/log"
	"github.com/mongodb/mongo-tools/common/options"
	"github.com/mongodb/mongo-tools/common/testutil"
	"github.com/mongodb/mongo-tools/common/util"

	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func init() {
	// bump up the verbosity to make checking debug log output possible
	log.SetVerbosity(&options.Verbosity{
		Verbose: []bool{true, true, true, true},
	})
}

var (
	testServer = "localhost"
	testPort   = db.DefaultTestPort
)

func TestMongorestore(t *testing.T) {
	ssl := testutil.GetSSLOptions()
	auth := testutil.GetAuthOptions()

	testutil.VerifyTestType(t, testutil.IntegrationTestType)
	toolOptions := &options.ToolOptions{
		Connection: &options.Connection{
			Host: testServer,
			Port: testPort,
		},
		Auth:          &auth,
		SSL:           &ssl,
		Namespace:     &options.Namespace{},
		HiddenOptions: &options.HiddenOptions{},
	}
	inputOptions := &InputOptions{}
	outputOptions := &OutputOptions{
		NumParallelCollections: 1,
		NumInsertionWorkers:    1,
		WriteConcern:           "majority",
	}
	Convey("With a test MongoRestore", t, func() {
		provider, err := db.NewSessionProvider(*toolOptions)
		if err != nil {
			log.Logf(log.Always, "error connecting to host: %v", err)
			os.Exit(util.ExitError)
		}
		restore := MongoRestore{
			ToolOptions:     toolOptions,
			OutputOptions:   outputOptions,
			InputOptions:    inputOptions,
			SessionProvider: provider,
		}
		session, _ := provider.GetSession()
		c1 := session.DB("db1").C("c1")
		c1.DropCollection()
		Convey("and an explicit target restores from that dump directory", func() {
			restore.TargetDirectory = "testdata/testdirs"
			err = restore.Restore()
			So(err, ShouldBeNil)
			count, err := c1.Count()
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 100)
		})

		Convey("and an target of '-' restores from standard input", func() {
			bsonFile, err := os.Open("testdata/testdirs/db1/c1.bson")
			toolOptions.Namespace.Collection = "c1"
			toolOptions.Namespace.DB = "db1"
			So(err, ShouldBeNil)
			restore.stdin = bsonFile
			restore.TargetDirectory = "-"
			err = restore.Restore()
			So(err, ShouldBeNil)
			count, err := c1.Count()
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 100)
		})

	})
}
