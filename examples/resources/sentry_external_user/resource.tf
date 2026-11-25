data "sentry_organization_integration" "github" {
  organization = "my-organization"

  provider_key = "github"
  name         = "my-github-organization"
}

# Resolve the "@octocat" CODEOWNERS handle to a Sentry user
resource "sentry_external_user" "octocat" {
  organization   = "my-organization"
  user_id        = "12345" # the Sentry user ID, not the organization member ID
  provider_name  = "github"
  external_name  = "@octocat"
  integration_id = data.sentry_organization_integration.github.id
}
