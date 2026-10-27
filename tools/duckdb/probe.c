#include <duckdb.h>
#include <stdio.h>
#include <string.h>

/* Small native smoke probe for the actual worker CPU, independent of Go caches. */
int main(void) {
    duckdb_database db = NULL;
    duckdb_connection conn = NULL;
    duckdb_result result;
    int code = 1;
    const char *version = duckdb_library_version();
    if (strcmp(version, "v2.0.0") != 0) {
        fprintf(stderr, "unexpected DuckDB version: %s\n", version);
        return 1;
    }
    if (duckdb_open(NULL, &db) == DuckDBError) goto done;
    if (duckdb_connect(db, &conn) == DuckDBError) goto done;
    if (duckdb_query(conn, "SELECT sum(i)::BIGINT FROM range(100) r(i)", &result) == DuckDBError) {
        fprintf(stderr, "query failed: %s\n", duckdb_result_error(&result));
    } else if (duckdb_row_count(&result) == 1 && duckdb_value_int64(&result, 0, 0) == 4950) {
        printf("DuckDB %s native query PASS: sum=4950\n", version);
        code = 0;
    }
    duckdb_destroy_result(&result);
done:
    if (conn) duckdb_disconnect(&conn);
    if (db) duckdb_close(&db);
    return code;
}
