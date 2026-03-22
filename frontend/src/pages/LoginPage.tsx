import { useState, useCallback, type FormEvent } from "react";
import { useNavigate, Link } from "react-router-dom";
import { useAuth } from "../contexts/AuthContext";
import { Turnstile } from "../components/Turnstile";

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: "flex",
    justifyContent: "center",
    alignItems: "center",
    minHeight: "100vh",
    backgroundColor: "#1a1a2e",
    color: "#e0e0e0",
  },
  card: {
    background: "#16213e",
    padding: "2rem",
    borderRadius: "8px",
    width: "100%",
    maxWidth: "400px",
    boxShadow: "0 4px 24px rgba(0,0,0,0.3)",
  },
  title: {
    textAlign: "center" as const,
    marginBottom: "1.5rem",
    fontSize: "1.5rem",
    color: "#e94560",
  },
  field: {
    marginBottom: "1rem",
  },
  label: {
    display: "block",
    marginBottom: "0.25rem",
    fontSize: "0.875rem",
    color: "#a0a0b0",
  },
  input: {
    width: "100%",
    padding: "0.5rem",
    borderRadius: "4px",
    border: "1px solid #333",
    background: "#0f3460",
    color: "#e0e0e0",
    fontSize: "1rem",
    boxSizing: "border-box" as const,
  },
  button: {
    width: "100%",
    padding: "0.75rem",
    borderRadius: "4px",
    border: "none",
    background: "#e94560",
    color: "#fff",
    fontSize: "1rem",
    cursor: "pointer",
    marginTop: "0.5rem",
  },
  error: {
    background: "#3d1111",
    color: "#ff6b6b",
    padding: "0.5rem",
    borderRadius: "4px",
    marginBottom: "1rem",
    fontSize: "0.875rem",
  },
  link: {
    display: "block",
    textAlign: "center" as const,
    marginTop: "1rem",
    color: "#e94560",
    fontSize: "0.875rem",
  },
};

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [turnstileToken, setTurnstileToken] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();
  const auth = useAuth();

  const handleTurnstileToken = useCallback((token: string) => {
    setTurnstileToken(token);
  }, []);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    if (auth.turnstileSiteKey && !turnstileToken) {
      setError("Please complete the captcha");
      return;
    }
    setSubmitting(true);
    try {
      await auth.login(email, password, turnstileToken || undefined);
      navigate("/");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.card}>
        <h1 style={styles.title}>HLL RADAR - Login</h1>
        {error && <div style={styles.error}>{error}</div>}
        <form onSubmit={handleSubmit}>
          <div style={styles.field}>
            <label style={styles.label}>Email</label>
            <input
              style={styles.input}
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoComplete="email"
            />
          </div>
          <div style={styles.field}>
            <label style={styles.label}>Password</label>
            <input
              style={styles.input}
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </div>
          {auth.turnstileSiteKey && (
            <Turnstile
              siteKey={auth.turnstileSiteKey}
              onToken={handleTurnstileToken}
              onExpire={() => setTurnstileToken("")}
            />
          )}
          <button style={styles.button} type="submit" disabled={submitting}>
            {submitting ? "Logging in..." : "Log In"}
          </button>
        </form>
        <Link to="/signup" style={styles.link}>
          Don't have an account? Sign up
        </Link>
      </div>
    </div>
  );
}
