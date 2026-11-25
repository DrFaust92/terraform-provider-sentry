package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/jianyuan/terraform-plugin-framework-utils/fwtypes"
	"github.com/jianyuan/terraform-provider-sentry/internal/apiclient"
	"github.com/jianyuan/terraform-provider-sentry/internal/diagutils"
	intresource "github.com/jianyuan/terraform-provider-sentry/internal/resource"
	"github.com/jianyuan/terraform-provider-sentry/internal/sentryclient"
)

type ExternalUserResourceModel struct {
	Id            types.String `tfsdk:"id"`
	Organization  types.String `tfsdk:"organization"`
	UserId        types.String `tfsdk:"user_id"`
	ProviderName  types.String `tfsdk:"provider_name"`
	ExternalName  types.String `tfsdk:"external_name"`
	IntegrationId types.String `tfsdk:"integration_id"`
	ExternalId    types.String `tfsdk:"external_id"`
}

func (m *ExternalUserResourceModel) Fill(ctx context.Context, data apiclient.ExternalActor) (diags diag.Diagnostics) {
	m.Id = types.StringValue(data.Id)
	// Update responses leave out userId, so keep the known value in that case.
	if data.UserId != nil {
		m.UserId = types.StringValue(*data.UserId)
	}
	m.ProviderName = types.StringValue(data.Provider)
	m.ExternalName = types.StringValue(data.ExternalName)
	m.IntegrationId = types.StringValue(data.IntegrationId)
	m.ExternalId = types.StringPointerValue(data.ExternalId)

	return
}

func (m ExternalUserResourceModel) toRequest(update bool) (apiclient.ExternalUserRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	userId, d := parseNumericId(path.Root("user_id"), m.UserId)
	diags.Append(d...)
	integrationId, d := parseNumericId(path.Root("integration_id"), m.IntegrationId)
	diags.Append(d...)

	body := apiclient.ExternalUserRequest{
		UserId:        userId,
		Provider:      m.ProviderName.ValueString(),
		ExternalName:  m.ExternalName.ValueString(),
		IntegrationId: integrationId,
	}
	if update {
		body.ExternalId = externalIdUpdateValue(m.ExternalId)
	} else {
		body.ExternalId = externalIdCreateValue(m.ExternalId)
	}

	return body, diags
}

var _ resource.Resource = &ExternalUserResource{}
var _ resource.ResourceWithConfigure = &ExternalUserResource{}
var _ resource.ResourceWithImportState = &ExternalUserResource{}

func NewExternalUserResource() resource.Resource {
	return &ExternalUserResource{}
}

type ExternalUserResource struct {
	baseResource
}

func (r *ExternalUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_external_user"
}

func (r *ExternalUserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sentry External User. Links a Sentry user to a user in an external provider, so that CODEOWNERS handles such as `@octocat` resolve to the Sentry user, or so that Slack and Microsoft Teams identities map to the Sentry user. " + externalActorFeatureNote,

		Attributes: map[string]schema.Attribute{
			"id": ResourceIdAttribute(),
			"organization": schema.StringAttribute{
				MarkdownDescription: "The slug of the organization the user belongs to.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the Sentry user to link. This is the user ID, not the organization member ID. The user must be a member of the organization.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(numericIdRegexp, "must be a numeric ID"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"provider_name": externalActorProviderNameAttribute(),
			"external_name": externalActorExternalNameAttribute(
				"The name of the user in the external provider, such as `@octocat`. GitHub, GitHub Enterprise, GitLab and Cursor names must start with `@`.",
			),
			"integration_id": externalActorIntegrationIdAttribute(),
			"external_id": externalActorExternalIdAttribute(
				"The ID of the user in the external provider, such as a Slack member ID.",
			),
		},
	}
}

func (r *ExternalUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ExternalUserResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := data.toRequest(false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	httpResp, err := r.apiClient.CreateOrganizationExternalUserWithResponse(
		ctx,
		data.Organization.ValueString(),
		body,
	)
	if err != nil {
		resp.Diagnostics.Append(diagutils.NewClientError("create", err))
		return
	} else if httpResp.StatusCode() == http.StatusOK && httpResp.JSON200 != nil {
		// Sentry returns 200 with the existing mapping instead of creating a new one. Adopting it silently
		// would let two resources manage the same mapping, so ask the user to import it instead.
		existing := *httpResp.JSON200
		resp.Diagnostics.AddError(
			"External user already exists",
			fmt.Sprintf(
				"Sentry already has an external user mapping for user %q, provider %q, integration %q and external name %q "+
					"(mapping ID %q). Import it with ID \"%s/%s\", or delete it before creating a new one.",
				data.UserId.ValueString(), existing.Provider, existing.IntegrationId, existing.ExternalName, existing.Id,
				data.Organization.ValueString(), existing.Id,
			),
		)
		return
	} else if httpResp.StatusCode() != http.StatusCreated || httpResp.JSON201 == nil {
		resp.Diagnostics.Append(diagutils.NewClientStatusError("create", httpResp.StatusCode(), httpResp.Body))
		return
	}

	resp.Diagnostics.Append(data.Fill(ctx, *httpResp.JSON201)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ExternalUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ExternalUserResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no endpoint to retrieve a single external user, so read the mapping through the
	// organization members list with `?expand=externalUsers`. Narrow the list to the user when it is
	// known; after an import only the mapping ID is known, so scan the members that have mappings.
	query := "hasExternalUsers:true"
	if fwtypes.IsKnown(data.UserId) {
		query = "user.id:" + data.UserId.ValueString()
	}
	params := &apiclient.ListOrganizationMembersParams{
		Query:  &query,
		Expand: new("externalUsers"),
	}

	var found *apiclient.ExternalActor
out:
	for {
		httpResp, err := r.apiClient.ListOrganizationMembersWithResponse(ctx, data.Organization.ValueString(), params)
		if err != nil {
			resp.Diagnostics.Append(diagutils.NewClientError("read", err))
			return
		} else if httpResp.StatusCode() == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		} else if httpResp.StatusCode() != http.StatusOK || httpResp.JSON200 == nil {
			resp.Diagnostics.Append(diagutils.NewClientStatusError("read", httpResp.StatusCode(), httpResp.Body))
			return
		}

		for _, member := range *httpResp.JSON200 {
			if member.ExternalUsers == nil {
				continue
			}
			for _, externalUser := range *member.ExternalUsers {
				if externalUser.Id == data.Id.ValueString() {
					found = &externalUser
					break out
				}
			}
		}

		params.Cursor = sentryclient.ParseNextPaginationCursor(httpResp.HTTPResponse)
		if params.Cursor == nil {
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(data.Fill(ctx, *found)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ExternalUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ExternalUserResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := data.toRequest(true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	httpResp, err := r.apiClient.UpdateOrganizationExternalUserWithResponse(
		ctx,
		data.Organization.ValueString(),
		data.Id.ValueString(),
		body,
	)
	if err != nil {
		resp.Diagnostics.Append(diagutils.NewClientError("update", err))
		return
	} else if httpResp.StatusCode() != http.StatusOK || httpResp.JSON200 == nil {
		resp.Diagnostics.Append(diagutils.NewClientStatusError("update", httpResp.StatusCode(), httpResp.Body))
		return
	}

	resp.Diagnostics.Append(data.Fill(ctx, *httpResp.JSON200)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ExternalUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ExternalUserResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	httpResp, err := r.apiClient.DeleteOrganizationExternalUserWithResponse(
		ctx,
		data.Organization.ValueString(),
		data.Id.ValueString(),
	)
	if err != nil {
		resp.Diagnostics.Append(diagutils.NewClientError("delete", err))
		return
	} else if httpResp.StatusCode() != http.StatusNoContent && httpResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.Append(diagutils.NewClientStatusError("delete", httpResp.StatusCode(), httpResp.Body))
		return
	}
}

func (r *ExternalUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	intresource.ImportState2PartPath("organization", "id")(ctx, req, resp)
}
