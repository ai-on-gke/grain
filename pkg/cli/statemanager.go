// statemanager.go manages the controller-local configuration repository
// for the daemon and its UI. One manager owns the open repository used by
// native grain/config pull requests and the export loop. Its lock keeps a
// merged configuration import from racing an on-demand or timer export.
package cli
