// stateresets.go retains notices for configuration changes that reset
// values. The config repository is controller-local, so there is no
// external pull request on which the deployment can post them; returning
// ErrNowhereToAnnounce tells pkg/staterepo to keep the notice for the UI
// and journal instead.
package cli
