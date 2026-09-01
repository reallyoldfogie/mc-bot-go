This repos uses protobuf-go to parse Minecraft protocol definitions and generate Go code for packets etc.

* data/gen_packet.go and data/gen_data generate the desired code, located in data/<version>, where <version> is specified by the Versions variable in data/gen_data.go.
* Code changes should be in  generator code, not generated code (generated code changes will be overwritten)
* Type information comes from data/generated/<version>/downloads/protocol.json, and is parsed by protobuf-go
* regenerate code using `go generate`
* check build status by running `go vet ./...` before doing `go build`
