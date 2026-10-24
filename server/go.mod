module github.com/adiazpar/re-discipline/server

go 1.26

toolchain go1.26.4

require (
	github.com/KimMachineGun/automemlimit v1.0.1
	github.com/adiazpar/re-discipline/retrieval v0.0.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.8.0
	github.com/klauspost/compress v1.20.3
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.21.0
)

require (
	al.essio.dev/pkg/shellescape v1.5.1 // indirect
	github.com/common-fate/httpsig v0.2.1 // indirect
	github.com/danieljoos/wincred v1.2.2 // indirect
	github.com/dunglas/httpsfv v1.0.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/gofrs/flock v0.13.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pbnjay/memory v0.0.0-20210728143218-7b4eea64cf58 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/zalando/go-keyring v0.2.6 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.56.0 // indirect
)

replace github.com/adiazpar/re-discipline/retrieval => ../retrieval
