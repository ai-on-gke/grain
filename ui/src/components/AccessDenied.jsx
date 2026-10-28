import GrainMark from "./GrainMark.jsx";

const MARK_SIZE = 120;

// AccessDenied is the screen in place of the app for a browser that may
// not use it. signedOut is a browser the server names no actor for: the
// access code is grain's one credential (docs/access-code.md), so the
// screen points at its sign-in page -- which is what the daemon serves
// for any page loaded without a session -- rather than asking for a name
// (docs/design/settings-panel.md §6.1). refusal and identityNotAccepted
// are /api/config's own reasons, when it gives them.
export default function AccessDenied({
  user,
  signedOut = false,
  refusal = "",
  identityNotAccepted = "",
}) {
  if (signedOut) {
    return (
      <div className="access-denied-screen" data-testid="signed-out">
        <GrainMark size={MARK_SIZE} title="grain" />
        <div className="access-denied-content">
          <h2>Signed out</h2>
          <p>
            This browser is not signed in. Sign in with this deployment's access
            code: <code>grain access-code</code> prints it on the machine grain
            runs on.
          </p>
          {refusal && <p data-testid="single-tenant-refusal">{refusal}</p>}
          {identityNotAccepted && (
            <p data-testid="identity-not-accepted">{identityNotAccepted}</p>
          )}
          <p>
            <a href="/">Enter access code</a>
          </p>
        </div>
      </div>
    );
  }
  return (
    <div className="access-denied-screen">
      <GrainMark size={MARK_SIZE} title="grain" />
      <div className="access-denied-content">
        <h2>Access Denied</h2>
        <p>You do not have access to this server and need to request it.</p>
        {user && (
          <>
            <p className="access-denied-user">
              Logged in as <code>{user}</code>
            </p>
            <p>
              To let you in, an admin adds exactly <code>{user}</code> to the
              access list.
            </p>
          </>
        )}
      </div>
    </div>
  );
}
