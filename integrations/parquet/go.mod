module github.com/infinityabundance/ekbloom-go/integrations/parquet

go 1.23

require (
	github.com/infinityabundance/ekbloom-go v0.0.0
	github.com/klauspost/compress v1.17.11
	github.com/parquet-go/parquet-go v0.25.1
)

require (
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	golang.org/x/sys v0.21.0 // indirect
)

replace github.com/infinityabundance/ekbloom-go => ../../
