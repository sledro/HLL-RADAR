package auth

import "context"

type contextKey string

const (
	contextUserID  contextKey = "user_id"
	contextOrgID   contextKey = "org_id"
	contextOrgRole contextKey = "org_role"
)

func SetAuthContext(ctx context.Context, userID, orgID int64, role string) context.Context {
	ctx = context.WithValue(ctx, contextUserID, userID)
	ctx = context.WithValue(ctx, contextOrgID, orgID)
	ctx = context.WithValue(ctx, contextOrgRole, role)
	return ctx
}

func UserIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(contextUserID).(int64)
	return v, ok
}

func OrgIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(contextOrgID).(int64)
	return v, ok
}

func RoleFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(contextOrgRole).(string)
	return v, ok
}

func IsOwner(ctx context.Context) bool {
	role, ok := RoleFromContext(ctx)
	return ok && role == "owner"
}
