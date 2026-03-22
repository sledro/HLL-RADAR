import { useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { apiClient } from "../services/api";

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
};

export function InviteAcceptPage() {
  const { token } = useParams<{ token: string }>();
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!token) {
      setError("Invalid invite link");
      return;
    }
    setError("");
    setSubmitting(true);
    try {
      const result = await apiClient.acceptInvite(token, password, displayName);
      localStorage.setItem("access_token", result.access_token);
      localStorage.setItem("refresh_token", result.refresh_token);
      localStorage.setItem("user", JSON.stringify(result.user));
      localStorage.setItem("org", JSON.stringify(result.org));
      navigate("/");
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to accept invite"
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.card}>
        <h1 style={styles.title}>Accept Invite</h1>
        <p style={{ textAlign: "center", marginBottom: "1rem", color: "#a0a0b0" }}>
          Set up your account to join the organization.
        </p>
        {error && <div style={styles.error}>{error}</div>}
        <form onSubmit={handleSubmit}>
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
          <button style={styles.button} type="submit" disabled={submitting}>
            {submitting ? "Setting up..." : "Accept & Join"}
          </button>
        </form>
      </div>
    </div>
  );
}
