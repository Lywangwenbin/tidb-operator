package operator

import (
	"testing"
	"time"

	tsql "github.com/ffan/tidb-operator/pkg/util/mysqlutil"
	_ "github.com/go-sql-driver/mysql"
)

func TestDb_Migrate(t *testing.T) {
	db, err := GetDb("006-xinyang1")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.stopMigrator(); err != nil {
		t.Error(err)
	}
	time.Sleep(6 * time.Second)
	src := tsql.Mysql{
		Database: "xinyang1",
		IP:       "10.213.124.195",
		Port:     13306,
		User:     "root",
		Password: "EJq4dspojdY3FmVF?TYVBkEMB",
	}
	if err = db.Migrate(src, "", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second)
}
