import { useState, type FormEvent } from "react";
import { useNavigate, Link } from "react-router-dom";
import { useAuth } from "../contexts/AuthContext";

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

export function SignupPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [orgName, setOrgName] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();
  const auth = useAuth();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    setSubmitting(true);
    try {
      await auth.signup(email, password, displayName, orgName);
      navigate("/");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Signup failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.card}>
        <h1 style={styles.title}>HLL RADAR - Sign Up</h1>
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
              minLength={8}
              autoComplete="new-password"
            />
          </div>
          <div style={styles.field}>
            <label style={styles.label}>Display Name</label>
            <input
              style={styles.input}
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
            />
          </div>
          <div style={styles.field}>
            <label style={styles.label}>Organization Name</label>
            <input
              style={styles.input}
              type="text"
              value={orgName}
              onChange={(e) => setOrgName(e.target.value)}
              required
            />
          </div>
          <button style={styles.button} type="submit" disabled={submitting}>
            {submitting ? "Creating account..." : "Sign Up"}
          </button>
        </form>
        <Link to="/login" style={styles.link}>
          Already have an account? Log in
        </Link>
      </div>
    </div>
  );
}
