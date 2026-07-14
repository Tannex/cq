// Sentinel module: keeps go build/test ./... in the parent module from
// descending into sidecar/node_modules, where npm packages may ship Go files.
module github.com/Tannex/cq/sidecar

go 1.26
