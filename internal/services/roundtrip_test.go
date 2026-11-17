package services

import (
	"strconv"
	"strings"
	"testing"

	"dbviewer/internal/model"
	"dbviewer/internal/query"
)

// Round trip: every displayed value, written back as a new row through
// ApplyChanges, must store exactly the original value (audit "value round-trips").

func wireValue(v any) any {
	switch x := v.(type) {
	case query.Binary:
		return map[string]any{"$type": "binary", "hex": x.Hex, "truncated": x.Truncated}
	case query.LongText:
		return map[string]any{"$type": "text", "text": x.Text, "truncated": x.Truncated}
	case int64:
		if x > 1<<53 || x < -(1<<53) {
			return x
		}
		return float64(x) // what JSON from the frontend looks like
	}
	return v
}

func roundTrip(t *testing.T, f *fixture, connID, table, castFmt string, cols []string) {
	p := f.page(connID, table, "")
	for ri, row := range p.Rows {
		vals := map[string]any{}
		for i, c := range p.Columns {
			vals[c.Name] = wireValue(row[i])
		}
		vals["id"] = float64(100 + ri)
		r, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: connID, Table: table, Inserts: []model.RowInsert{{Values: vals}}})
		if err != nil || !r.OK {
			t.Errorf("re-insert row %d: %v %q", ri, err, r.Error)
			continue
		}
		for _, c := range cols {
			expr := strings.ReplaceAll(castFmt, "%", c)
			a := f.scalar(connID, "SELECT "+expr+" FROM "+table+" WHERE id = "+strconv.Itoa(ri+1))
			b := f.scalar(connID, "SELECT "+expr+" FROM "+table+" WHERE id = "+strconv.Itoa(100+ri))
			if a != b {
				t.Errorf("row %d col %s: original %v, copy %v", ri+1, c, a, b)
			}
		}
	}
}

func TestRegrRoundTripPostgres(t *testing.T) {
	e := engineByName(t, "postgres")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_rt")
	f.exec(c.ID, `CREATE TABLE regr_rt (id int PRIMARY KEY, tstz timestamptz, ts timestamp, d date, tm time, tmz timetz, iv interval, arr text[], iarr int[],
		u uuid, bt bit(4), vb varbit, m money, by bytea, j json, jb jsonb, n numeric, f4 real, f8 double precision, bo boolean, i8 bigint, inet inet, ch char(4), x xml, pt point, rng int4range)`)
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_rt") })
	f.exec(c.ID, `INSERT INTO regr_rt VALUES
	 (1, '2025-01-31 13:45:00.123456+02', '2025-01-31 13:45:00.5', '2025-01-31', '13:45:00.25', '13:45:00+02', '1 year 2 mons 3 days 04:05:06', '{a,"b c",NULL,"q\"x"}', '{1,2}',
	  'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', B'1010', B'101', '1234.56', '\x00ff10', '{"b":1,  "a":2}', '{"b":1,  "a":2}', '12345678901234567890.123456789000', 0.1, 0.1, true, 9223372036854775807, '10.0.0.1/24', 'ab', '<a>1</a>', '(1,2)', '[1,5)'),
	 (2, 'infinity', '-infinity', '0044-03-15 BC', '24:00:00', '00:00:00-08', '-1 day', '{}', NULL,
	  NULL, B'0000', B'', '-0.01', '', 'null', 'null', 'NaN', 'NaN', '-Infinity', false, -9223372036854775808, '::1', '', NULL, NULL, 'empty'),
	 (3, '1900-01-01 00:00:00+00', '10000-01-01 00:00:00', '9999-12-31', '00:00:00', '23:59:59.999999+14', '00:00:00', '{"{}"}', '{{1,2},{3,4}}',
	  NULL, B'1111', B'1', '0', '\xdeadbeef', '"str"', '[1, 2.50]', '1e-20', 1e20, 1e308, NULL, 9007199254740993, NULL, NULL, NULL, NULL, NULL)`)
	roundTrip(t, f, c.ID, "regr_rt", "(%)::text", []string{"tstz", "ts", "d", "tm", "tmz", "iv", "arr", "iarr", "u", "bt", "vb", "m", "by", "j", "jb", "n", "f4", "f8", "bo", "i8", "inet", "ch", "x", "pt", "rng"})
}

func TestRegrRoundTripMySQL(t *testing.T) {
	e := engineByName(t, "mysql")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_rt")
	f.exec(c.ID, `CREATE TABLE regr_rt (id int PRIMARY KEY, dt6 datetime(6), ts timestamp(3) NULL, d date, tm time(2), y year, de decimal(40,20), ub bigint unsigned, sb bigint,
		f float, db double, bi binary(4), vbin varbinary(8), bl blob, b8 bit(8), b64 bit(64), flag tinyint(1), j json, e enum('a','b'), st set('x','y'), tx text, vc varchar(20), ti tinyint unsigned)`)
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_rt") })
	f.exec(c.ID, `INSERT INTO regr_rt VALUES
	 (1, '2025-01-31 13:45:00.123456', '2025-01-31 13:45:00.123', '2025-01-31', '-838:59:59.00', 2024, '12345678901234567890.12345678901234567890', 18446744073709551615, -9223372036854775808,
	  0.1, 0.1, x'00ff1000', x'68656c6c6f', x'00', b'01000001', b'1111111111111111111111111111111111111111111111111111111111111111', 1, '{"b": 1, "a": [1, 2.50]}', 'a', 'x,y', 'line1\nline2\\ ''q''', '', 255),
	 (2, '1000-01-01 00:00:00', NULL, '9999-12-31', '00:00:00', 1901, '-0.00000000000000000001', 9007199254740993, 9007199254740993,
	  1e20, 1e308, x'41424344', x'', x'', b'00110001', b'0', 0, 'null', 'b', '', '', 'NULL', 0),
	 (3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, b'00000000', NULL, 2, NULL, NULL, NULL, NULL, NULL, NULL)`)
	roundTrip(t, f, c.ID, "regr_rt", "HEX(CAST(% AS BINARY))", []string{"dt6", "ts", "d", "tm", "y", "de", "ub", "sb", "f", "db", "bi", "vbin", "bl", "b8", "b64", "flag", "j", "e", "st", "tx", "vc", "ti"})
}
