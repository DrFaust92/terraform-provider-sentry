package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/jianyuan/terraform-provider-sentry/internal/acctest"
	"github.com/jianyuan/terraform-provider-sentry/internal/apiclient"
	"github.com/jianyuan/terraform-provider-sentry/internal/resourceid"
)

// testAccExternalUserUserId returns the user ID of a member of the test organization.
func testAccExternalUserUserId(t *testing.T) string {
	t.Helper()

	httpResp, err := acctest.SharedApiClient.ListOrganizationMembersWithResponse(context.Background(), acctest.TestOrganization, &apiclient.ListOrganizationMembersParams{})
	if err != nil {
		t.Fatalf("failed to list organization members: %s", err)
	} else if httpResp.StatusCode() != http.StatusOK || httpResp.JSON200 == nil {
		t.Fatalf("failed to list organization members: status code=%d, body=%s", httpResp.StatusCode(), httpResp.Body)
	}
	for _, member := range *httpResp.JSON200 {
		if member.User.Id != "" {
			return member.User.Id
		}
	}
	t.Fatal("the test organization has no member with a user account")
	return ""
}

func TestAccExternalUserResource_basic(t *testing.T) {
	// Check the environment before looking up a user, so the test skips instead of failing.
	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skipf("Acceptance tests skipped unless env '%s' set", resource.EnvTfAcc)
	}
	acctest.PreCheck(t)
	if acctest.TestGitHubInstallationId == "" {
		t.Skip("Skipping test due to missing SENTRY_TEST_GITHUB_INSTALLATION_ID environment variable")
	}

	rn := "sentry_external_user.test"
	userId := testAccExternalUserUserId(t)
	externalName := "@" + acctest.RandomWithPrefix("tf-user")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccExternalUserResourceConfig(userId, externalName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("organization"), knownvalue.StringExact(acctest.TestOrganization)),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("user_id"), knownvalue.StringExact(userId)),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("provider_name"), knownvalue.StringExact("github")),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_name"), knownvalue.StringExact(externalName)),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("integration_id"), knownvalue.StringExact(acctest.TestGitHubInstallationId)),
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_id"), knownvalue.Null()),
				},
			},
			{
				Config: testAccExternalUserResourceConfig(userId, externalName+"-renamed"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(rn, tfjsonpath.New("external_name"), knownvalue.StringExact(externalName+"-renamed")),
				},
			},
			{
				ResourceName:      rn,
				ImportState:       true,
				ImportStateIdFunc: resourceid.ImportState2PartIDFunc(rn, "organization", "id"),
				ImportStateVerify: true,
			},
		},
	})
}

func testAccExternalUserResourceConfig(userId, externalName string) string {
	return fmt.Sprintf(`
resource "sentry_external_user" "test" {
	organization   = %[1]q
	user_id        = %[2]q
	provider_name  = "github"
	external_name  = %[3]q
	integration_id = %[4]q
}
`, acctest.TestOrganization, userId, externalName, acctest.TestGitHubInstallationId)
}
