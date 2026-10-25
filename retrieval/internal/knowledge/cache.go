package knowledge

// Disposable per-project bookkeeping, preserved across ordinary index rebuilds.
// No historical document text or additional database is stored.
var CacheTables = map[string]string{
	"upload_files":   `CREATE TABLE IF NOT EXISTS upload_files(path TEXT PRIMARY KEY,id TEXT NOT NULL,fingerprint TEXT NOT NULL,stamp TEXT NOT NULL,verified INTEGER NOT NULL,format TEXT NOT NULL,seen TEXT NOT NULL)`,
	"finding_stamps": `CREATE TABLE IF NOT EXISTS finding_stamps(id TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,format TEXT NOT NULL)`,
	"copy_bases":     `CREATE TABLE IF NOT EXISTS copy_bases(service TEXT NOT NULL,community TEXT NOT NULL,id TEXT NOT NULL,fingerprint TEXT NOT NULL,sequence INTEGER NOT NULL,format TEXT NOT NULL,PRIMARY KEY(service,community,id))`,
}
