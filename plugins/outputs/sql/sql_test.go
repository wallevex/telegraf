package sql

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/testutil"
)

func stableMetric(
	name string,
	tags []telegraf.Tag,
	fields []telegraf.Field,
	tm time.Time,
	tp ...telegraf.ValueType,
) telegraf.Metric {
	// We want to compare the output of this plugin with expected
	// output. Maps don't preserve order so comparison fails. There's
	// no metric constructor that takes a slice of tag and slice of
	// field, just the one that takes maps.
	//
	// To preserve order, construct the metric without tags and fields
	// and then add them using AddTag and AddField.  Those are stable.
	m := metric.New(name, map[string]string{}, map[string]interface{}{}, tm, tp...)
	for _, tag := range tags {
		m.AddTag(tag.Key, tag.Value)
	}
	for _, field := range fields {
		m.AddField(field.Key, field.Value)
	}
	return m
}

var (
	// 2021-05-17T22:04:45+00:00
	// or 2021-05-17T16:04:45-06:00
	ts = time.Unix(1621289085, 0).UTC()

	testMetrics = []telegraf.Metric{
		stableMetric(
			"metric_one",
			[]telegraf.Tag{
				{
					Key:   "tag_one",
					Value: "tag1",
				},
				{
					Key:   "tag_two",
					Value: "tag2",
				},
			},
			[]telegraf.Field{
				{
					Key:   "int64_one",
					Value: int64(1234),
				},
				{
					Key:   "int64_two",
					Value: int64(2345),
				},
				{
					Key:   "bool_one",
					Value: true,
				},
				{
					Key:   "bool_two",
					Value: false,
				},
				{
					Key:   "uint64_one",
					Value: uint64(1000000000),
				},
				{
					Key:   "float64_one",
					Value: float64(3.1415),
				},
			},
			ts,
		),
		stableMetric(
			"metric_two",
			[]telegraf.Tag{
				{
					Key:   "tag_three",
					Value: "tag3",
				},
			},
			[]telegraf.Field{
				{
					Key:   "string_one",
					Value: "string1",
				},
			},
			ts,
		),
		stableMetric( // test spaces in metric, tag, and field names
			"metric three",
			[]telegraf.Tag{
				{
					Key:   "tag four",
					Value: "tag4",
				},
			},
			[]telegraf.Field{
				{
					Key:   "string two",
					Value: "string2",
				},
			},
			ts,
		),
	}

	postCreateMetrics = []telegraf.Metric{
		stableMetric(
			"metric_one",
			[]telegraf.Tag{
				{
					Key:   "tag_add_after_create",
					Value: "tag2",
				},
			},
			[]telegraf.Field{
				{
					Key:   "bool_add_after_create",
					Value: true,
				},
			},
			ts,
			telegraf.Untyped,
		),
	}
)

func TestMysqlIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/mariadb/initdb/script.sql")
	require.NoError(t, err)

	// initdb/script.sql creates this database
	const dbname = "foo"

	// The mariadb image lets you set the root password through an env
	// var. We'll use root to insert and query test data.
	const username = "root"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "3306"
	container := testutil.Container{
		Image: "mariadb",
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	address := config.NewSecret([]byte(fmt.Sprintf("%v:%v@tcp(%v:%v)/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:            "mysql",
		DataSourceName:    address,
		Convert:           defaultConvert,
		InitSQL:           "SET sql_mode='ANSI_QUOTES';",
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
	}
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))

	files := []string{
		"./testdata/mariadb/expected_metric_one.sql",
		"./testdata/mariadb/expected_metric_two.sql",
		"./testdata/mariadb/expected_metric_three.sql",
	}
	for _, fn := range files {
		expected, err := os.ReadFile(fn)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			rc, out, err := container.Exec([]string{
				"bash",
				"-c",
				"mariadb-dump --user=" + username +
					" --password=" + password +
					" --compact" +
					" --skip-opt " +
					" --skip-no-autocommit " +
					dbname,
			})
			require.NoError(t, err)
			require.Equal(t, 0, rc)

			b, err := io.ReadAll(out)
			require.NoError(t, err)
			return bytes.Contains(b, expected)
		}, 10*time.Second, 500*time.Millisecond, fn)
	}
}

func TestMysqlUpdateSchemeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/mariadb/initdb/script.sql")
	require.NoError(t, err)

	// initdb/script.sql creates this database
	const dbname = "foo"

	// The mariadb image lets you set the root password through an env
	// var. We'll use root to insert and query test data.
	const username = "root"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "3306"
	container := testutil.Container{
		Image: "mariadb",
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	address := config.NewSecret([]byte(fmt.Sprintf("%v:%v@tcp(%v:%v)/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:              "mysql",
		DataSourceName:      address,
		Convert:             defaultConvert,
		InitSQL:             "SET sql_mode='ANSI_QUOTES';",
		TimestampColumn:     "timestamp",
		ConnectionMaxIdle:   2,
		Log:                 testutil.Logger{},
		TableUpdateTemplate: "ALTER TABLE {TABLE} ADD COLUMN {COLUMN}",
	}
	require.NoError(t, p.Init())
	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))
	// Write a metric that targets the same table but has additional fields
	// to test the automatic column update functionality.
	require.NoError(t, p.Write(postCreateMetrics))

	fields := []string{
		"`tag_add_after_create` text DEFAULT NULL",
		"`bool_add_after_create` tinyint(1) DEFAULT NULL",
	}
	for _, column := range fields {
		require.Eventually(t, func() bool {
			rc, out, err := container.Exec([]string{
				"bash",
				"-c",
				"mariadb-dump --user=" + username +
					" --password=" + password +
					" --compact" +
					" --skip-opt " +
					dbname,
			})
			require.NoError(t, err)
			require.Equal(t, 0, rc)

			b, err := io.ReadAll(out)
			require.NoError(t, err)

			return bytes.Contains(b, []byte(column))
		}, 10*time.Second, 500*time.Millisecond, column)
	}
}

func TestMysqlIntegrationSendBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/mariadb/initdb/script.sql")
	require.NoError(t, err)

	// initdb/script.sql creates this database
	const dbname = "foo"

	// The mariadb image lets you set the root password through an env
	// var. We'll use root to insert and query test data.
	const username = "root"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "3306"
	container := testutil.Container{
		Image: "mariadb",
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	address := config.NewSecret([]byte(fmt.Sprintf("%v:%v@tcp(%v:%v)/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:            "mysql",
		DataSourceName:    address,
		Convert:           defaultConvert,
		InitSQL:           "SET sql_mode='ANSI_QUOTES';",
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
		BatchTx:           true,
	}
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))

	files := []string{
		"./testdata/mariadb/expected_metric_one.sql",
		"./testdata/mariadb/expected_metric_two.sql",
		"./testdata/mariadb/expected_metric_three.sql",
	}
	for _, fn := range files {
		expected, err := os.ReadFile(fn)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			rc, out, err := container.Exec([]string{
				"bash",
				"-c",
				"mariadb-dump --user=" + username +
					" --password=" + password +
					" --compact" +
					" --skip-opt " +
					" --skip-no-autocommit " +
					dbname,
			})
			require.NoError(t, err)
			require.Equal(t, 0, rc)

			b, err := io.ReadAll(out)
			require.NoError(t, err)

			return bytes.Contains(b, expected)
		}, 10*time.Second, 500*time.Millisecond, fn)
	}
}

func TestPostgresIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/postgres/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// default username for postgres is postgres
	const username = "postgres"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "5432"
	container := testutil.Container{
		Image: "postgres",
		Env: map[string]string{
			"POSTGRES_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("postgres://%v:%v@%v:%v/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:            "pgx",
		DataSourceName:    address,
		Convert:           defaultConvert,
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
	}
	p.Convert.Real = "double precision"
	p.Convert.Unsigned = "bigint"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	defer p.Close()
	require.NoError(t, p.Write(testMetrics))
	require.NoError(t, p.Close())

	expected, err := os.ReadFile("./testdata/postgres/expected.sql")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rc, out, err := container.Exec([]string{
			"bash",
			"-c",
			"pg_dump" +
				" --username=" + username +
				" --no-comments" +
				" " + dbname +
				// pg_dump's output has comments that include build info
				// of postgres and pg_dump. The build info changes with
				// each release. To prevent these changes from causing the
				// test to fail, we strip out comments. Also strip out
				// blank lines.
				"|grep -E -v '(^--|^$|^SET )'",
		})
		require.NoError(t, err)
		require.Equal(t, 0, rc)

		b, err := io.ReadAll(out)
		require.NoError(t, err)

		return bytes.Contains(b, expected)
	}, 5*time.Second, 500*time.Millisecond)
}

func TestPostgresUpdateSchemeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/postgres/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// default username for postgres is postgres
	const username = "postgres"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "5432"
	container := testutil.Container{
		Image: "postgres",
		Env: map[string]string{
			"POSTGRES_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("postgres://%v:%v@%v:%v/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:              "pgx",
		DataSourceName:      address,
		Convert:             defaultConvert,
		TimestampColumn:     "timestamp",
		ConnectionMaxIdle:   2,
		Log:                 testutil.Logger{},
		TableUpdateTemplate: "ALTER TABLE {TABLE} ADD COLUMN {COLUMN}",
	}
	p.Convert.Real = "double precision"
	p.Convert.Unsigned = "bigint"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	defer p.Close()
	require.NoError(t, p.Write(testMetrics))
	// Write a metric that targets the same table but has additional fields
	// to test the automatic column update functionality.
	require.NoError(t, p.Write(postCreateMetrics))
	require.NoError(t, p.Close())

	fields := []string{
		"tag_add_after_create text",
		"bool_add_after_create boolean",
	}
	for _, column := range fields {
		require.Eventually(t, func() bool {
			rc, out, err := container.Exec([]string{
				"bash",
				"-c",
				"pg_dump" +
					" --username=" + username +
					" --no-comments" +
					" " + dbname +
					// pg_dump's output has comments that include build info
					// of postgres and pg_dump. The build info changes with
					// each release. To prevent these changes from causing the
					// test to fail, we strip out comments. Also strip out
					// blank lines.
					"|grep -E -v '(^--|^$|^SET )'",
			})
			require.NoError(t, err)
			require.Equal(t, 0, rc)

			b, err := io.ReadAll(out)
			require.NoError(t, err)

			return bytes.Contains(b, []byte(column))
		}, 5*time.Second, 500*time.Millisecond, column)
	}
}

func TestPostgresIntegrationSendBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/postgres/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// default username for postgres is postgres
	const username = "postgres"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "5432"
	container := testutil.Container{
		Image: "postgres",
		Env: map[string]string{
			"POSTGRES_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("postgres://%v:%v@%v:%v/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:            "pgx",
		DataSourceName:    address,
		Convert:           defaultConvert,
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
		BatchTx:           true,
	}
	p.Convert.Real = "double precision"
	p.Convert.Unsigned = "bigint"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	defer p.Close()
	require.NoError(t, p.Write(testMetrics))
	require.NoError(t, p.Close())

	expected, err := os.ReadFile("./testdata/postgres/expected.sql")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rc, out, err := container.Exec([]string{
			"bash",
			"-c",
			"pg_dump" +
				" --username=" + username +
				" --no-comments" +
				" " + dbname +
				// pg_dump's output has comments that include build info
				// of postgres and pg_dump. The build info changes with
				// each release. To prevent these changes from causing the
				// test to fail, we strip out comments. Also strip out
				// blank lines.
				"|grep -E -v '(^--|^$|^SET )'",
		})
		require.NoError(t, err)
		require.Equal(t, 0, rc)

		b, err := io.ReadAll(out)
		require.NoError(t, err)

		return bytes.Contains(b, expected)
	}, 5*time.Second, 500*time.Millisecond)
}

func TestClickHouseIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logConfig, err := filepath.Abs("testdata/clickhouse/enable_stdout_log.xml")
	require.NoError(t, err)

	initdb, err := filepath.Abs("testdata/clickhouse/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// username for connecting to clickhouse
	const username = "clickhouse"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "9000"
	container := testutil.Container{
		Image:        "clickhouse",
		ExposedPorts: []string{servicePort, "8123"},
		Env: map[string]string{
			"CLICKHOUSE_USER":     "clickhouse",
			"CLICKHOUSE_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql":                initdb,
			"/etc/clickhouse-server/config.d/enable_stdout_log.xml": logConfig,
			"/out": outDir,
		},
		WaitingFor: wait.ForAll(
			wait.NewHTTPStrategy("/").WithPort(nat.Port("8123")),
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("Ready for connections"),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("tcp://%s:%s/%s?username=%s&password=%s",
		container.Address, container.Ports[servicePort], dbname, username, password)))
	p := &SQL{
		Driver:            "clickhouse",
		DataSourceName:    address,
		Convert:           defaultConvert,
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
	}
	p.Convert.Integer = "Int64"
	p.Convert.Text = "String"
	p.Convert.Timestamp = "DateTime"
	p.Convert.Defaultvalue = "String"
	p.Convert.Unsigned = "UInt64"
	p.Convert.Bool = "UInt8"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))

	cases := []struct {
		table    string
		expected string
	}{
		{"metric_one", "`float64_one` Float64"},
		{"metric_two", "`string_one` String"},
		{"metric three", "`string two` String"},
	}
	for _, tc := range cases {
		require.Eventually(t, func() bool {
			var out io.Reader
			_, out, err = container.Exec([]string{
				"bash",
				"-c",
				"clickhouse-client" +
					" --user=" + username +
					" --database=" + dbname +
					" --format=TabSeparatedRaw" +
					" --multiquery" +
					` --query="SELECT * FROM \"` + tc.table + `\"; SHOW CREATE TABLE \"` + tc.table + `\""`,
			})
			require.NoError(t, err)
			b, err := io.ReadAll(out)
			require.NoError(t, err)
			return bytes.Contains(b, []byte(tc.expected))
		}, 5*time.Second, 500*time.Millisecond)
	}
}

func TestClickHouseUpdateSchemeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logConfig, err := filepath.Abs("testdata/clickhouse/enable_stdout_log.xml")
	require.NoError(t, err)

	initdb, err := filepath.Abs("testdata/clickhouse/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// username for connecting to clickhouse
	const username = "clickhouse"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "9000"
	container := testutil.Container{
		Image:        "clickhouse",
		ExposedPorts: []string{servicePort, "8123"},
		Env: map[string]string{
			"CLICKHOUSE_USER":     "clickhouse",
			"CLICKHOUSE_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql":                initdb,
			"/etc/clickhouse-server/config.d/enable_stdout_log.xml": logConfig,
			"/out": outDir,
		},
		WaitingFor: wait.ForAll(
			wait.NewHTTPStrategy("/").WithPort(nat.Port("8123")),
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("Ready for connections"),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("tcp://%s:%s/%s?username=%s&password=%s",
		container.Address, container.Ports[servicePort], dbname, username, password)))
	p := &SQL{
		Driver:              "clickhouse",
		DataSourceName:      address,
		Convert:             defaultConvert,
		TimestampColumn:     "timestamp",
		ConnectionMaxIdle:   2,
		Log:                 testutil.Logger{},
		TableUpdateTemplate: "ALTER TABLE {TABLE} ADD COLUMN {COLUMN}",
	}
	p.Convert.Integer = "Int64"
	p.Convert.Text = "String"
	p.Convert.Timestamp = "DateTime"
	p.Convert.Defaultvalue = "String"
	p.Convert.Unsigned = "UInt64"
	p.Convert.Bool = "UInt8"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))
	// Write a metric that targets the same table but has additional fields
	// to test the automatic column update functionality.
	require.NoError(t, p.Write(postCreateMetrics))

	fields := []string{
		"`tag_add_after_create` String",
		"`bool_add_after_create` UInt8",
	}
	for _, column := range fields {
		require.Eventually(t, func() bool {
			var out io.Reader
			_, out, err = container.Exec([]string{
				"bash",
				"-c",
				"clickhouse-client" +
					" --user=" + username +
					" --database=" + dbname +
					" --format=TabSeparatedRaw" +
					" --multiquery" +
					` --query="SELECT * FROM "metric_one"; SHOW CREATE TABLE "metric_one""`,
			})
			require.NoError(t, err)
			b, err := io.ReadAll(out)
			require.NoError(t, err)
			return bytes.Contains(b, []byte(column))
		}, 5*time.Second, 500*time.Millisecond, column)
	}
}

func TestClickHouseDsnConvert(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Contains no incompatible settings - no change
		{
			"tcp://host1:1234,host2:1234/database?password=p&username=u",
			"tcp://host1:1234,host2:1234/database?password=p&username=u",
		},
		// connection_open_strategy + read_timeout with values that are already v2 compatible
		{
			"tcp://host1:1234,host2:1234/database?connection_open_strategy=in_order&read_timeout=2.5s&username=u",
			"tcp://host1:1234,host2:1234/database?connection_open_strategy=in_order&read_timeout=2.5s&username=u",
		},
		// Preserve invalid URLs
		{
			"://this will not parse",
			"://this will not parse",
		},
		// Removing incompatible parameters
		{
			"tcp://host:1234/database?no_delay=true&username=u",
			"tcp://host:1234/database?username=u",
		},
		// read_timeout + alt_hosts
		{
			"tcp://host1:1234/database?read_timeout=2.5&alt_hosts=host2:2345&username=u",
			"tcp://host1:1234,host2:2345/database?read_timeout=2.5s&username=u",
		},
		// database
		{
			"tcp://host1:1234?database=db&username=u",
			"tcp://host1:1234/db?username=u",
		},
	}

	for _, tt := range tests {
		plugin := &SQL{
			Driver:         "clickhouse",
			DataSourceName: config.NewSecret([]byte(tt.input)),
			Log:            testutil.Logger{},
		}
		require.NoError(t, plugin.Init())
		resolvedSecret, err := plugin.DataSourceName.Get()
		require.NoError(t, err)
		resolvedDsn := resolvedSecret.String()
		resolvedSecret.Destroy()
		require.Equal(t, tt.expected, resolvedDsn)
	}
}

func TestClickHouseIntegrationSendBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logConfig, err := filepath.Abs("testdata/clickhouse/enable_stdout_log.xml")
	require.NoError(t, err)

	initdb, err := filepath.Abs("testdata/clickhouse/initdb/init.sql")
	require.NoError(t, err)

	// initdb/init.sql creates this database
	const dbname = "foo"

	// username for connecting to clickhouse
	const username = "clickhouse"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "9000"
	container := testutil.Container{
		Image:        "clickhouse",
		ExposedPorts: []string{servicePort, "8123"},
		Env: map[string]string{
			"CLICKHOUSE_USER":     "clickhouse",
			"CLICKHOUSE_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql":                initdb,
			"/etc/clickhouse-server/config.d/enable_stdout_log.xml": logConfig,
			"/out": outDir,
		},
		WaitingFor: wait.ForAll(
			wait.NewHTTPStrategy("/").WithPort(nat.Port("8123")),
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("Ready for connections"),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	// host, port, username, password, dbname
	address := config.NewSecret([]byte(fmt.Sprintf("tcp://%s:%s/%s?username=%s&password=%s",
		container.Address, container.Ports[servicePort], dbname, username, password)))
	p := &SQL{
		Driver:            "clickhouse",
		DataSourceName:    address,
		Convert:           defaultConvert,
		TimestampColumn:   "timestamp",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
		BatchTx:           true,
	}
	p.Convert.Integer = "Int64"
	p.Convert.Text = "String"
	p.Convert.Timestamp = "DateTime"
	p.Convert.Defaultvalue = "String"
	p.Convert.Unsigned = "UInt64"
	p.Convert.Bool = "UInt8"
	p.Convert.ConversionStyle = "literal"
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))

	cases := []struct {
		table    string
		expected string
	}{
		{"metric_one", "`float64_one` Float64"},
		{"metric_two", "`string_one` String"},
		{"metric three", "`string two` String"},
	}
	for _, tc := range cases {
		require.Eventually(t, func() bool {
			var out io.Reader
			_, out, err = container.Exec([]string{
				"bash",
				"-c",
				"clickhouse-client" +
					" --user=" + username +
					" --database=" + dbname +
					" --format=TabSeparatedRaw" +
					" --multiquery" +
					` --query="SELECT * FROM \"` + tc.table + `\"; SHOW CREATE TABLE \"` + tc.table + `\""`,
			})
			require.NoError(t, err)
			b, err := io.ReadAll(out)
			require.NoError(t, err)
			return bytes.Contains(b, []byte(tc.expected))
		}, 5*time.Second, 500*time.Millisecond)
	}
}

func TestMysqlEmptyTimestampColumnIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	initdb, err := filepath.Abs("testdata/mariadb_no_timestamp/initdb/script.sql")
	require.NoError(t, err)

	// initdb/script.sql creates this database
	const dbname = "foo"

	// The mariadb image lets you set the root password through an env
	// var. We'll use root to insert and query test data.
	const username = "root"

	password := testutil.GetRandomString(32)
	outDir := t.TempDir()

	servicePort := "3306"
	container := testutil.Container{
		Image: "mariadb",
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": password,
		},
		Files: map[string]string{
			"/docker-entrypoint-initdb.d/script.sql": initdb,
			"/out":                                   outDir,
		},
		ExposedPorts: []string{servicePort},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(servicePort)),
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		),
	}
	require.NoError(t, container.Start(), "failed to start container")
	defer container.Terminate()

	// use the plugin to write to the database
	address := config.NewSecret([]byte(fmt.Sprintf("%v:%v@tcp(%v:%v)/%v",
		username, password, container.Address, container.Ports[servicePort], dbname,
	)))
	p := &SQL{
		Driver:            "mysql",
		DataSourceName:    address,
		Convert:           defaultConvert,
		InitSQL:           "SET sql_mode='ANSI_QUOTES';",
		ConnectionMaxIdle: 2,
		Log:               testutil.Logger{},
	}
	require.NoError(t, p.Init())

	require.NoError(t, p.Connect())
	require.NoError(t, p.Write(testMetrics))

	files := []string{
		"./testdata/mariadb_no_timestamp/expected_metric_one.sql",
		"./testdata/mariadb_no_timestamp/expected_metric_two.sql",
		"./testdata/mariadb_no_timestamp/expected_metric_three.sql",
	}
	for _, fn := range files {
		expected, err := os.ReadFile(fn)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			rc, out, err := container.Exec([]string{
				"bash",
				"-c",
				"mariadb-dump --user=" + username +
					" --password=" + password +
					" --compact" +
					" --skip-opt " +
					dbname,
			})
			require.NoError(t, err)
			require.Equal(t, 0, rc)

			b, err := io.ReadAll(out)
			require.NoError(t, err)

			return bytes.Contains(b, expected)
		}, 10*time.Second, 500*time.Millisecond, fn)
	}
}

func TestTimestampOnUpdateSchema(t *testing.T) {
	p := &SQL{
		Log:     testutil.Logger{},
		Convert: defaultConvert,
	}
	expected := defaultConvert.Timestamp
	results := p.deriveDatatype(ts)
	require.Equal(t, expected, results)
}
