//go:build !nooracle

package tools

import (
	"net/url"
	"testing"

	"github.com/sijms/go-ora/v2/network"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func TestOracleAddressKeepsSpecialSignsOfThePasswordInPlace(t *testing.T) {
	conn := networkConn("oracle")
	conn.Port = 1521
	conn.Database = "FREEPDB1"
	dsn, err := oracleDSN(conn, "p@ss/w:rd#1", "/project")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Scheme != "oracle" || u.Host != "db.example.com:1521" || u.User.Username() != "ana@corp" || password != "p@ss/w:rd#1" || u.Path != "/FREEPDB1" {
		t.Errorf("dsn = %s", dsn)
	}
}

func TestOracleAddressFollowsTheTLSMode(t *testing.T) {
	conn := networkConn("oracle")
	for mode, want := range map[string][2]string{
		config.TLSVerify: {"true", "true"}, config.TLSRequire: {"true", "false"}, config.TLSDisable: {"", ""},
	} {
		conn.TLS = mode
		dsn, _ := oracleDSN(conn, "", "/project")
		q := mustQuery(t, dsn)
		if q.Get("SSL") != want[0] || q.Get("SSL VERIFY") != want[1] {
			t.Errorf("%s: %v", mode, q)
		}
	}
}

func TestOracleErrorsThatMayPassAreTold(t *testing.T) {
	for code, want := range map[int]bool{12514: true, 12541: true, 1033: true, 3113: true, 60: true, 1: false, 942: false, 1017: false} {
		if got := oracleTransient(network.NewOracleError(code)); got != want {
			t.Errorf("ORA-%05d: %v", code, got)
		}
	}
	if oracleTransient(errPlain) {
		t.Error("a plain error was taken for one that may pass")
	}
}
