package web

import (
	"context"
	"testing"
)

func TestOpenOPCUAClientNoEndpoint(t *testing.T) {
	if _, err := openOPCUAClient(context.Background(), map[string]string{}); err == nil {
		t.Fatal("expected error for missing endpoint URL")
	}
}

func TestOpenOPCUAClientUnreachable(t *testing.T) {
	_, err := openOPCUAClient(context.Background(), map[string]string{"endpoint_url": "opc.tcp://127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected error for unreachable opcua endpoint")
	}
}
