//go:build !unix

package executor

// agentTokenNoFollow is 0 where the platform has no O_NOFOLLOW; O_EXCL still
// refuses an existing name. run_as_user is Linux only.
const agentTokenNoFollow = 0
