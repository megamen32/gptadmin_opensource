package server

import (
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
	"testing"
)

func TestCatalogConfigDigestEffectiveHTTPCredentials(t *testing.T) {
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "first-test-value")
	agent := supervisor.Agent{Transport: "http", Headers: map[string]string{"Authorization": "Bearer ${GPTADMIN_CATALOG_TEST_TOKEN}"}}
	first := mcpCatalogConfigDigest(agent)
	if first != mcpCatalogConfigDigest(agent) {
		t.Fatal("unchanged input must be stable")
	}
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "second-test-value")
	if first == mcpCatalogConfigDigest(agent) {
		t.Fatal("effective HTTP credentials must invalidate metadata")
	}
}

func TestCatalogConfigDigestInheritedStdioCredentials(t *testing.T) {
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "first-test-value")
	agent := supervisor.Agent{Transport: "stdio"}
	first := mcpCatalogConfigDigest(agent)
	if first != mcpCatalogConfigDigest(agent) {
		t.Fatal("unchanged environment must be stable")
	}
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "second-test-value")
	if first == mcpCatalogConfigDigest(agent) {
		t.Fatal("inherited stdio credentials must invalidate metadata")
	}
}

func TestCatalogConfigDigestExpandedStdioEnvironment(t *testing.T) {
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "first-test-value")
	agent := supervisor.Agent{Transport: "stdio", Env: map[string]string{"TOKEN": "${GPTADMIN_CATALOG_TEST_TOKEN}"}}
	first := mcpCatalogConfigDigest(agent)
	t.Setenv("GPTADMIN_CATALOG_TEST_TOKEN", "second-test-value")
	if first == mcpCatalogConfigDigest(agent) {
		t.Fatal("expanded stdio credentials must invalidate metadata")
	}
}
