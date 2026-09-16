// Package version holds surviva's own release version, a single source of
// truth for both `surviva version` and anything else that needs to report
// it.
package version

// Version is surviva's release version, MAJOR.MINOR: the major number bumps
// for significant/breaking changes, the minor number for everything else.
// Bumped by hand per release, matching a git branch of the same name --
// there is no build-time injection (no CI/release pipeline exists yet).
const Version = "v1.3"
