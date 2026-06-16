// Package pb holds the generated gRPC/protobuf code for the marshald Daemon
// service. Do not edit the generated *.pb.go files by hand; regenerate with
// `go generate ./internal/pb`.
package pb

//go:generate protoc --go_out=../.. --go_opt=module=marshal --go-grpc_out=../.. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto
