package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// newExternalUserTestServer fakes an organization with two members: user 7 on the first page and
// user 8 on the second. The members list supports `query=user.id:<id>` and `query=hasExternalUsers:true`,
// and includes each member's mappings when called with `expand=externalUsers`.
func newExternalUserTestServer(t *testing.T, store *fakeExternalActorStore, scans *atomic.Int32) *httptest.Server {
	var server *httptest.Server
	server = newFakeSentryServer(t, func(mux *http.ServeMux) {
		member := func(r *http.Request, userId string) map[string]any {
			m := map[string]any{
				"id":      "m" + userId,
				"email":   "user" + userId + "@example.com",
				"name":    "user" + userId,
				"pending": false,
				"expired": false,
				"orgRole": "member",
				"user":    map[string]any{"id": userId},
			}
			if r.URL.Query().Get("expand") == "externalUsers" {
				m["externalUsers"] = store.list("userId", userId)
			}
			return m
		}

		mux.HandleFunc("GET /api/0/organizations/test-org/members/", func(w http.ResponseWriter, r *http.Request) {
			switch q := r.URL.Query().Get("query"); q {
			case "user.id:7", "user.id:8":
				_ = json.NewEncoder(w).Encode([]any{member(r, q[len("user.id:"):])})
			case "hasExternalUsers:true":
				scans.Add(1)
				if r.URL.Query().Get("cursor") == "" {
					w.Header().Set("Link", fmt.Sprintf(
						`<%[1]s/api/0/organizations/test-org/members/?cursor=0:0:1>; rel="previous"; results="false"; cursor="0:0:1", `+
							`<%[1]s/api/0/organizations/test-org/members/?cursor=0:1:0>; rel="next"; results="true"; cursor="0:1:0"`,
						server.URL,
					))
					_ = json.NewEncoder(w).Encode([]any{member(r, "7")})
				} else {
					_ = json.NewEncoder(w).Encode([]any{member(r, "8")})
				}
			default:
				_ = json.NewEncoder(w).Encode([]any{})
			}
		})
		mux.HandleFunc("POST /api/0/organizations/test-org/external-users/", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Unlike external teams, Sentry includes the user when matching an existing mapping.
			store.create(w, body, "userId", fmt.Sprint(body["user_id"]), true)
		})
		mux.HandleFunc("PUT /api/0/organizations/test-org/external-users/{id}/", func(w http.ResponseWriter, r *http.Request) {
			store.update(w, r, r.PathValue("id"), "userId")
		})
		mux.HandleFunc("DELETE /api/0/organizations/test-org/external-users/{id}/", func(w http.ResponseWriter, r *http.Request) {
			store.delete(w, r.PathValue("id"))
		})
	})
	return server
}

func testExternalUserUnitConfig(baseUrl string, externalName string) string {
	return fmt.Sprintf(`
provider "sentry" {
	base_url = "%[1]s/api/"
	token    = "test"
}

resource "sentry_external_user" "test" {
	organization   = "test-org"
	user_id        = "8"
	provider_name  = "github"
	external_name  = %[2]q
	integration_id = "123"
}
`, baseUrl, externalName)
}

func TestExternalUserResource_lifecycle(t *testing.T) {
	rn := "sentry_external_user.test"
	store := newFakeExternalActorStore()
	var scans atomic.Int32
	server := newExternalUserTestServer(t, store, &scans)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testExternalUserUnitConfig(server.URL, "@octocat"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("id"), knownvalue.StringExact("42")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("user_id"), knownvalue.StringExact("8")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("provider_name"), knownvalue.StringExact("github")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_name"), knownvalue.StringExact("@octocat")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("integration_id"), knownvalue.StringExact("123")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_id"), knownvalue.Null()),
				},
			},
			{
				Config: testExternalUserUnitConfig(server.URL, "@octocat-renamed"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("id"), knownvalue.StringExact("42")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("user_id"), knownvalue.StringExact("8")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_name"), knownvalue.StringExact("@octocat-renamed")),
				},
			},
			{
				// Only the mapping ID is known on import, so the member that owns it (on the second page) is found by scanning.
				ResourceName:      rn,
				ImportState:       true,
				ImportStateId:     "test-org/42",
				ImportStateVerify: true,
			},
		},
	})

	if store.creates != 1 {
		t.Fatalf("expected exactly one create, got %d", store.creates)
	}
	if scans.Load() == 0 {
		t.Fatal("expected the import to scan members with external users")
	}
	if n := len(store.list("userId", "8")); n != 0 {
		t.Fatalf("expected the mapping to be deleted, %d left", n)
	}
}

func TestExternalUserResource_alreadyExists(t *testing.T) {
	store := newFakeExternalActorStore()
	store.actors["5"] = map[string]any{
		"id":            "5",
		"provider":      "github",
		"externalName":  "@octocat",
		"integrationId": "123",
		"userId":        "8",
	}
	var scans atomic.Int32
	server := newExternalUserTestServer(t, store, &scans)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testExternalUserUnitConfig(server.URL, "@octocat"),
				ExpectError: regexp.MustCompile(`(?s)External user already exists.*"test-org/5"`),
			},
		},
	})

	if _, ok := store.actors["5"]; !ok {
		t.Fatal("the existing mapping must not be deleted")
	}
}
