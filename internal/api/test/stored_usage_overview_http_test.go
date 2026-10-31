package test

import (
	"net/http"
	"testing"
)

func TestStoredOverviewHTTPRouteExists(t *testing.T) {
	db := openAPITestDatabase(t)
	r, a, _ := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	res := serveAPIGet(r, "/api/v1/usage/stored/overview?range=24h", a)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured stored overview: status=%d body=%s", res.Code, res.Body.String())
	}
}
