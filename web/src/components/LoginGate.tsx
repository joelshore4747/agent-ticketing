import { createSignal } from 'solid-js';
import { login } from '../api/client';

/** Shown in place of the whole app once a request has come back 401 (TP-320): nothing on screen
 *  works without a credential, so nothing but this field does either, until one is entered. */
export function LoginGate() {
  const [value, setValue] = createSignal('');

  return (
    <div class="login-gate">
      <form
        class="dialog__panel"
        onSubmit={(event) => {
          event.preventDefault();
          const entered = value().trim();
          if (entered) login(entered);
        }}
      >
        <h2>Paste your ticketing token</h2>
        <p class="meta">Issued by scripts/issue-token.sh. Kept in this browser only.</p>
        <div class="field">
          <label for="login-token">Token</label>
          <input
            id="login-token"
            type="password"
            autofocus
            value={value()}
            onInput={(event) => setValue(event.currentTarget.value)}
          />
        </div>
        <div class="btn-row">
          <button type="submit" class="btn btn--primary" disabled={!value().trim()}>
            Log in
          </button>
        </div>
      </form>
    </div>
  );
}
