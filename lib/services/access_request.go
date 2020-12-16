/*
Copyright 2019 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package services

import (
	"context"
	"fmt"

	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/utils/parse"

	"github.com/gravitational/trace"

	"github.com/pborman/uuid"
)

// RequestStrategy is an indicator of how access requests
// should be handled for holders of a given role.
type RequestStrategy string

const (
	// RequestStrategyOptional is the default request strategy,
	// indicating that no special actions/requirements exist.
	RequestStrategyOptional RequestStrategy = "optional"

	// RequestStrategyReason indicates that client implementations
	// should automatically generate wildcard requests on login, and
	// users should be prompted for a reason.
	RequestStrategyReason RequestStrategy = "reason"

	// RequestStrategyAlways indicates that client implementations
	// should automatically generate wildcard requests on login, but
	// that reasons are not required.
	RequestStrategyAlways RequestStrategy = "always"
)

// ShouldAutoRequest checks if the request strategy
// indicates that a request should be automatically
// generated on login.
func (s RequestStrategy) ShouldAutoRequest() bool {
	switch s {
	case RequestStrategyReason, RequestStrategyAlways:
		return true
	default:
		return false
	}
}

// RequireReason checks if the request strategy
// is one that requires users to always supply
// reasons with their requests.
func (s RequestStrategy) RequireReason() bool {
	return s == RequestStrategyReason
}

// RequestIDs is a collection of IDs for privilege escalation requests.
type RequestIDs struct {
	AccessRequests []string `json:"access_requests,omitempty"`
}

func (r *RequestIDs) Marshal() ([]byte, error) {
	data, err := utils.FastMarshal(r)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return data, nil
}

func (r *RequestIDs) Unmarshal(data []byte) error {
	if err := utils.FastUnmarshal(data, r); err != nil {
		return trace.Wrap(err)
	}
	return trace.Wrap(r.Check())
}

func (r *RequestIDs) Check() error {
	for _, id := range r.AccessRequests {
		if uuid.Parse(id) == nil {
			return trace.BadParameter("invalid request id %q", id)
		}
	}
	return nil
}

func (r *RequestIDs) IsEmpty() bool {
	return len(r.AccessRequests) < 1
}

// AccessRequestUpdate encompasses the parameters of a
// SetAccessRequestState call.
type AccessRequestUpdate struct {
	// RequestID is the ID of the request to be updated.
	RequestID string
	// State is the state that the target request
	// should resolve to.
	State RequestState
	// Reason is an optional description of *why* the
	// the request is being resolved.
	Reason string
	// Annotations supplies extra data associated with
	// the resolution; primarily for audit purposes.
	Annotations map[string][]string
	// Roles, if non-empty declares a list of roles
	// that should override the role list of the request.
	// This parameter is only accepted on approvals
	// and must be a subset of the role list originally
	// present on the request.
	Roles []string
}

func (u *AccessRequestUpdate) Check() error {
	if u.RequestID == "" {
		return trace.BadParameter("missing request id")
	}
	if u.State.IsNone() {
		return trace.BadParameter("missing request state")
	}
	if len(u.Roles) > 0 && !u.State.IsApproved() {
		return trace.BadParameter("cannot override roles when setting state: %s", u.State)
	}
	return nil
}

// DynamicAccess is a service which manages dynamic RBAC.
type DynamicAccess interface {
	// CreateAccessRequest stores a new access request.
	CreateAccessRequest(ctx context.Context, req AccessRequest) error
	// SetAccessRequestState updates the state of an existing access request.
	SetAccessRequestState(ctx context.Context, params AccessRequestUpdate) error
	// GetAccessRequests gets all currently active access requests.
	GetAccessRequests(ctx context.Context, filter AccessRequestFilter) ([]AccessRequest, error)
	// DeleteAccessRequest deletes an access request.
	DeleteAccessRequest(ctx context.Context, reqID string) error
	// GetPluginData loads all plugin data matching the supplied filter.
	GetPluginData(ctx context.Context, filter PluginDataFilter) ([]PluginData, error)
	// UpdatePluginData updates a per-resource PluginData entry.
	UpdatePluginData(ctx context.Context, params PluginDataUpdateParams) error
}

// DynamicAccessExt is an extended dynamic access interface
// used to implement some auth server internals.
type DynamicAccessExt interface {
	DynamicAccess
	// UpsertAccessRequest creates or updates an access request.
	UpsertAccessRequest(ctx context.Context, req AccessRequest) error
	// DeleteAllAccessRequests deletes all existent access requests.
	DeleteAllAccessRequests(ctx context.Context) error
}

// GetAccessRequest is a helper function assists with loading a specific request by ID.
func GetAccessRequest(ctx context.Context, acc DynamicAccess, reqID string) (AccessRequest, error) {
	reqs, err := acc.GetAccessRequests(ctx, AccessRequestFilter{
		ID: reqID,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if len(reqs) < 1 {
		return nil, trace.NotFound("no access request matching %q", reqID)
	}
	return reqs[0], nil
}

// NewAccessRequest assembled an AccessReqeust resource.
func NewAccessRequest(user string, roles ...string) (AccessRequest, error) {
	req := AccessRequestV3{
		Kind:    KindAccessRequest,
		Version: V3,
		Metadata: Metadata{
			Name: uuid.New(),
		},
		Spec: AccessRequestSpecV3{
			User:  user,
			Roles: roles,
			State: RequestState_PENDING,
		},
	}
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return &req, nil
}

func (c AccessRequestConditions) GetTraitMappings() TraitMappingSet {
	tm := make([]TraitMapping, 0, len(c.ClaimsToRoles))
	for _, mapping := range c.ClaimsToRoles {
		tm = append(tm, TraitMapping{
			Trait: mapping.Claim,
			Value: mapping.Value,
			Roles: mapping.Roles,
		})
	}
	return TraitMappingSet(tm)
}

type UserAndRoleGetter interface {
	UserGetter
	RoleGetter
	GetRoles() ([]Role, error)
}

// appendRoleMatchers constructs all role matchers for a given
// AccessRequestConditions instance and appends them to the
// supplied matcher slice.
func appendRoleMatchers(matchers []parse.Matcher, conditions AccessRequestConditions, traits map[string][]string) ([]parse.Matcher, error) {
	// build matchers for the role list
	for _, r := range conditions.Roles {
		m, err := parse.NewMatcher(r)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		matchers = append(matchers, m)
	}

	// build matchers for all role mappings
	ms, err := conditions.GetTraitMappings().TraitsToRoleMatchers(traits)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return append(matchers, ms...), nil
}

// insertAnnotations constructs all annotations for a given
// AccessRequestConditions instance and adds them to the
// supplied annotations mapping.
func insertAnnotations(annotations map[string][]string, conditions AccessRequestConditions, traits map[string][]string) {
	for key, vals := range conditions.Annotations {
		// get any previous values at key
		allVals := annotations[key]

		// iterate through all new values and expand any
		// variable interpolation syntax they contain.
	ApplyTraits:
		for _, v := range vals {
			applied, err := applyValueTraits(v, traits)
			if err != nil {
				// skip values that failed variable expansion
				continue ApplyTraits
			}
			allVals = append(allVals, applied...)
		}

		annotations[key] = allVals
	}
}

// requestValidator a helper for validating access requests.
// a user's statically assigned roles are are "added" to the
// validator via the push() method, which extracts all the
// relevant rules, peforms variable substitutions, and builds
// a set of simple Allow/Deny datastructures.  These, in turn,
// are used to validate and expand the access request.
type requestValidator struct {
	traits        map[string][]string
	requireReason bool
	opts          struct {
		expandRoles, annotate bool
	}
	Roles struct {
		Allow, Deny []parse.Matcher
	}
	Annotations struct {
		Allow, Deny map[string][]string
	}
}

func newRequestValidator(traits map[string][]string, opts ...ValidateRequestOption) requestValidator {
	m := requestValidator{
		traits: traits,
	}
	for _, opt := range opts {
		opt(&m)
	}
	if m.opts.annotate {
		// validation process for incoming access requests requires
		// generating system annotations to be attached to the request
		// before it is inserted into the backend.
		m.Annotations.Allow = make(map[string][]string)
		m.Annotations.Deny = make(map[string][]string)
	}
	return m
}

// push compiles a role's configuration into the request validator.
// All of the requesint user's statically assigned roles must be pushed
// before validation begins.
func (m *requestValidator) push(role Role) error {
	var err error

	m.requireReason = m.requireReason || role.GetOptions().RequestAccess.RequireReason()

	allow, deny := role.GetAccessRequestConditions(Allow), role.GetAccessRequestConditions(Deny)

	m.Roles.Deny, err = appendRoleMatchers(m.Roles.Deny, deny, m.traits)
	if err != nil {
		return trace.Wrap(err)
	}

	m.Roles.Allow, err = appendRoleMatchers(m.Roles.Allow, allow, m.traits)
	if err != nil {
		return trace.Wrap(err)
	}

	if m.opts.annotate {
		// validation process for incoming access requests requires
		// generating system annotations to be attached to the request
		// before it is inserted into the backend.
		insertAnnotations(m.Annotations.Deny, deny, m.traits)
		insertAnnotations(m.Annotations.Allow, allow, m.traits)
	}
	return nil
}

// CanRequestRole checks if a given role can be requested.
func (m *requestValidator) CanRequestRole(name string) bool {
	for _, deny := range m.Roles.Deny {
		if deny.Match(name) {
			return false
		}
	}
	for _, allow := range m.Roles.Allow {
		if allow.Match(name) {
			return true
		}
	}
	return false
}

// SystemAnnotations calculates the system annotations for a pending
// access request.
func (m *requestValidator) SystemAnnotations() map[string][]string {
	annotations := make(map[string][]string)
	for k, va := range m.Annotations.Allow {
		var filtered []string
		for _, v := range va {
			if !utils.SliceContainsStr(m.Annotations.Deny[k], v) {
				filtered = append(filtered, v)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		annotations[k] = filtered
	}
	return annotations
}

type ValidateRequestOption func(*requestValidator)

// ExpandRoles activates expansion of wildcard role lists
// (`[]string{"*"}`) when true.
func ExpandRoles(expand bool) ValidateRequestOption {
	return func(v *requestValidator) {
		v.opts.expandRoles = expand
	}
}

// ApplySystemAnnotations causes system annotations to be computed
// and attached during validation when true.
func ApplySystemAnnotations(annotate bool) ValidateRequestOption {
	return func(v *requestValidator) {
		v.opts.annotate = annotate
	}
}

// ValidateAccessRequest validates an access request against the associated users's
// *statically assigned* roles. If expandRoles is true, it will also expand wildcard
// requests, setting their role list to include all roles the user is allowed to request.
// Expansion should be performed before an access request is initially placed in the backend.
func ValidateAccessRequest(getter UserAndRoleGetter, req AccessRequest, opts ...ValidateRequestOption) error {
	user, err := getter.GetUser(req.GetUser(), false)
	if err != nil {
		return trace.Wrap(err)
	}

	validator := newRequestValidator(user.GetTraits(), opts...)

	// load all statically assigned roles for the user and
	// use them to build our validation state.
	for _, roleName := range user.GetRoles() {
		role, err := getter.GetRole(roleName)
		if err != nil {
			return trace.Wrap(err)
		}
		if err := validator.push(role); err != nil {
			return trace.Wrap(err)
		}
	}

	if validator.requireReason && req.GetRequestReason() == "" {
		return trace.BadParameter("request reason must be specified (required by static role configuration)")
	}

	// check for "wildcard request" (`roles=*`).  wildcard requests
	// need to be expanded into a list consisting of all existing roles
	// that the user does not hold and is allowed to request.
	if r := req.GetRoles(); len(r) == 1 && r[0] == Wildcard {

		if !req.GetState().IsPending() {
			// expansion is only permitted in pending requests.  once resolved,
			// a request's role list must be immutable.
			return trace.BadParameter("wildcard requests are not permitted in state %s", req.GetState())
		}

		if !validator.opts.expandRoles {
			// teleport always validates new incoming pending access requests
			// with ExpandRoles(true). after that, it should be impossible to
			// add new values to the role list.
			return trace.BadParameter("unexpected wildcard request (this is a bug)")
		}

		allRoles, err := getter.GetRoles()
		if err != nil {
			return trace.Wrap(err)
		}

		var expanded []string
		for _, role := range allRoles {
			if n := role.GetName(); !utils.SliceContainsStr(user.GetRoles(), n) && validator.CanRequestRole(n) {
				// user does not currently hold this role, and is allowed to request it.
				expanded = append(expanded, n)
			}
		}
		if len(expanded) == 0 {
			return trace.BadParameter("no requestable roles, please verify static RBAC configuration")
		}
		req.SetRoles(expanded)
	}

	// verify that all requested roles are permissible
	for _, roleName := range req.GetRoles() {
		if !validator.CanRequestRole(roleName) {
			return trace.BadParameter("user %q can not request role %q", req.GetUser(), roleName)
		}
	}

	if validator.opts.annotate {
		// incoming requests must have system annotations attached before
		// before being inserted into the backend. this is how the
		// RBAC system propagates sideband information to plugins.
		req.SetSystemAnnotations(validator.SystemAnnotations())
	}

	return nil
}

type AccessRequestMarshaler interface {
	MarshalAccessRequest(req AccessRequest, opts ...MarshalOption) ([]byte, error)
	UnmarshalAccessRequest(bytes []byte, opts ...MarshalOption) (AccessRequest, error)
}

type accessRequestMarshaler struct{}

func (r *accessRequestMarshaler) MarshalAccessRequest(req AccessRequest, opts ...MarshalOption) ([]byte, error) {
	cfg, err := collectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	switch r := req.(type) {
	case *AccessRequestV3:
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			cp := *r
			cp.SetResourceID(0)
			r = &cp
		}
		return utils.FastMarshal(r)
	default:
		return nil, trace.BadParameter("unrecognized access request type: %T", req)
	}
}

func (r *accessRequestMarshaler) UnmarshalAccessRequest(data []byte, opts ...MarshalOption) (AccessRequest, error) {
	cfg, err := collectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var req AccessRequestV3
	if cfg.SkipValidation {
		if err := utils.FastUnmarshal(data, &req); err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		if err := utils.UnmarshalWithSchema(GetAccessRequestSchema(), &req, data); err != nil {
			return nil, trace.Wrap(err)
		}
	}
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	if cfg.ID != 0 {
		req.SetResourceID(cfg.ID)
	}
	if !cfg.Expires.IsZero() {
		req.SetExpiry(cfg.Expires)
	}
	return &req, nil
}

var accessRequestMarshalerInstance AccessRequestMarshaler = &accessRequestMarshaler{}

func GetAccessRequestMarshaler() AccessRequestMarshaler {
	marshalerMutex.Lock()
	defer marshalerMutex.Unlock()
	return accessRequestMarshalerInstance
}

const AccessRequestSpecSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"user": { "type": "string" },
		"roles": {
			"type": "array",
			"items": { "type": "string" }
		},
		"state": { "type": "integer" },
		"created": { "type": "string" },
		"expires": { "type": "string" },
		"request_reason": { "type": "string" },
		"resolve_reason": { "type": "string" },
		"resolve_annotations": { "type": "object" },
		"system_annotations": { "type": "object" }
	}
}`

func GetAccessRequestSchema() string {
	return fmt.Sprintf(V2SchemaTemplate, MetadataSchema, AccessRequestSpecSchema, DefaultDefinitions)
}
