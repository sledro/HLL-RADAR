import { useState, useEffect, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../contexts/AuthContext";
import { apiClient } from "../services/api";
import type { Server, OrgMember } from "../types";

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: "100vh",
    backgroundColor: "#1a1a2e",
    color: "#e0e0e0",
    padding: "2rem",
  },
  header: {
    display: "flex",
    alignItems: "center",
    gap: "1rem",
    marginBottom: "2rem",
  },
  backLink: {
    color: "#e94560",
    cursor: "pointer",
    background: "none",
    border: "none",
    fontSize: "1rem",
    textDecoration: "underline",
  },
  title: {
    fontSize: "1.5rem",
    color: "#e94560",
    margin: 0,
  },
  section: {
    background: "#16213e",
    borderRadius: "8px",
    padding: "1.5rem",
    marginBottom: "1.5rem",
    maxWidth: "800px",
  },
  sectionTitle: {
    fontSize: "1.2rem",
    marginBottom: "1rem",
    color: "#e0e0e0",
  },
  table: {
    width: "100%",
    borderCollapse: "collapse" as const,
  },
  th: {
    textAlign: "left" as const,
    padding: "0.5rem",
    borderBottom: "1px solid #333",
    color: "#a0a0b0",
    fontSize: "0.875rem",
  },
  td: {
    padding: "0.5rem",
    borderBottom: "1px solid #222",
    fontSize: "0.875rem",
  },
  addBtn: {
    padding: "0.5rem 1rem",
    borderRadius: "4px",
    border: "none",
    background: "#e94560",
    color: "#fff",
    cursor: "pointer",
    fontSize: "0.875rem",
    marginTop: "1rem",
  },
  removeBtn: {
    padding: "0.25rem 0.5rem",
    borderRadius: "4px",
    border: "none",
    background: "#c0392b",
    color: "#fff",
    cursor: "pointer",
    fontSize: "0.75rem",
  },
  testBtn: {
    padding: "0.25rem 0.5rem",
    borderRadius: "4px",
    border: "1px solid #e94560",
    background: "transparent",
    color: "#e94560",
    cursor: "pointer",
    fontSize: "0.75rem",
    marginRight: "0.5rem",
  },
  formRow: {
    display: "flex",
    gap: "0.5rem",
    marginBottom: "0.5rem",
    flexWrap: "wrap" as const,
  },
  formInput: {
    padding: "0.5rem",
    borderRadius: "4px",
    border: "1px solid #333",
    background: "#0f3460",
    color: "#e0e0e0",
    fontSize: "0.875rem",
    flex: "1",
    minWidth: "120px",
  },
  error: {
    background: "#3d1111",
    color: "#ff6b6b",
    padding: "0.5rem",
    borderRadius: "4px",
    marginBottom: "1rem",
    fontSize: "0.875rem",
  },
  success: {
    background: "#113d11",
    color: "#6bff6b",
    padding: "0.5rem",
    borderRadius: "4px",
    marginBottom: "1rem",
    fontSize: "0.875rem",
  },
};

export function OrgSettingsPage() {
  const navigate = useNavigate();
  const auth = useAuth();

  // Servers
  const [servers, setServers] = useState<Server[]>([]);
  const [showServerForm, setShowServerForm] = useState(false);
  const [serverForm, setServerForm] = useState({
    name: "",
    display_name: "",
    host: "",
    port: "",
    password: "",
  });
  const [serverError, setServerError] = useState("");
  const [serverSuccess, setServerSuccess] = useState("");
  const [testResults, setTestResults] = useState<Record<number, string>>({});

  // Members
  const [members, setMembers] = useState<OrgMember[]>([]);
  const [showInviteForm, setShowInviteForm] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteResult, setInviteResult] = useState("");
  const [memberError, setMemberError] = useState("");

  useEffect(() => {
    loadServers();
    loadMembers();
  }, []);

  const loadServers = async () => {
    try {
      const s = await apiClient.getServers();
      setServers(s || []);
    } catch {
      setServerError("Failed to load servers");
    }
  };

  const loadMembers = async () => {
    try {
      const m = await apiClient.getOrgMembers();
      setMembers(m || []);
    } catch {
      setMemberError("Failed to load members");
    }
  };

  const handleAddServer = async (e: FormEvent) => {
    e.preventDefault();
    setServerError("");
    setServerSuccess("");
    try {
      await apiClient.createServer({
        name: serverForm.name,
        display_name: serverForm.display_name,
        host: serverForm.host,
        port: parseInt(serverForm.port, 10),
        password: serverForm.password,
      });
      setServerForm({ name: "", display_name: "", host: "", port: "", password: "" });
      setShowServerForm(false);
      setServerSuccess("Server added");
      loadServers();
    } catch (err) {
      setServerError(err instanceof Error ? err.message : "Failed to add server");
    }
  };

  const handleRemoveServer = async (serverId: number) => {
    if (!confirm("Remove this server?")) return;
    try {
      await apiClient.deleteServer(serverId);
      loadServers();
    } catch (err) {
      setServerError(err instanceof Error ? err.message : "Failed to remove server");
    }
  };

  const handleTestServer = async (serverId: number) => {
    setTestResults((prev) => ({ ...prev, [serverId]: "testing..." }));
    try {
      const result = await apiClient.testServer(serverId);
      setTestResults((prev) => ({ ...prev, [serverId]: result.status }));
    } catch {
      setTestResults((prev) => ({ ...prev, [serverId]: "failed" }));
    }
  };

  const handleInvite = async (e: FormEvent) => {
    e.preventDefault();
    setMemberError("");
    setInviteResult("");
    try {
      const result = await apiClient.inviteAdmin(inviteEmail);
      setInviteResult(`Invite sent! URL: ${result.invite_url}`);
      setInviteEmail("");
      setShowInviteForm(false);
    } catch (err) {
      setMemberError(err instanceof Error ? err.message : "Failed to send invite");
    }
  };

  const handleRemoveMember = async (userId: number) => {
    if (!confirm("Remove this member?")) return;
    try {
      await apiClient.removeMember(userId);
      loadMembers();
    } catch (err) {
      setMemberError(err instanceof Error ? err.message : "Failed to remove member");
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <button style={styles.backLink} onClick={() => navigate("/")}>
          &larr; Back to Dashboard
        </button>
        <h1 style={styles.title}>
          Organization Settings{auth.org ? ` - ${auth.org.name}` : ""}
        </h1>
      </div>

      {/* Servers Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Servers</h2>
        {serverError && <div style={styles.error}>{serverError}</div>}
        {serverSuccess && <div style={styles.success}>{serverSuccess}</div>}
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Name</th>
              <th style={styles.th}>Host</th>
              <th style={styles.th}>Status</th>
              <th style={styles.th}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {servers.map((server) => (
              <tr key={server.id}>
                <td style={styles.td}>
                  {server.display_name || server.name}
                </td>
                <td style={styles.td}>
                  {server.host}:{server.port}
                </td>
                <td style={styles.td}>
                  {server.is_active ? "Active" : "Inactive"}
                  {testResults[server.id] && (
                    <span style={{ marginLeft: "0.5rem", color: testResults[server.id] === "connected" ? "#6bff6b" : "#ff6b6b" }}>
                      ({testResults[server.id]})
                    </span>
                  )}
                </td>
                <td style={styles.td}>
                  <button
                    style={styles.testBtn}
                    onClick={() => handleTestServer(server.id)}
                  >
                    Test
                  </button>
                  {auth.isOwner && (
                    <button
                      style={styles.removeBtn}
                      onClick={() => handleRemoveServer(server.id)}
                    >
                      Remove
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {servers.length === 0 && (
              <tr>
                <td style={styles.td} colSpan={4}>
                  No servers configured
                </td>
              </tr>
            )}
          </tbody>
        </table>

        {!showServerForm && (
          <button style={styles.addBtn} onClick={() => setShowServerForm(true)}>
            Add Server
          </button>
        )}

        {showServerForm && (
          <form onSubmit={handleAddServer} style={{ marginTop: "1rem" }}>
            <div style={styles.formRow}>
              <input
                style={styles.formInput}
                placeholder="Name (slug)"
                value={serverForm.name}
                onChange={(e) =>
                  setServerForm({ ...serverForm, name: e.target.value })
                }
                required
              />
              <input
                style={styles.formInput}
                placeholder="Display Name"
                value={serverForm.display_name}
                onChange={(e) =>
                  setServerForm({ ...serverForm, display_name: e.target.value })
                }
                required
              />
            </div>
            <div style={styles.formRow}>
              <input
                style={styles.formInput}
                placeholder="Host"
                value={serverForm.host}
                onChange={(e) =>
                  setServerForm({ ...serverForm, host: e.target.value })
                }
                required
              />
              <input
                style={{ ...styles.formInput, maxWidth: "100px" }}
                placeholder="Port"
                type="number"
                value={serverForm.port}
                onChange={(e) =>
                  setServerForm({ ...serverForm, port: e.target.value })
                }
                required
              />
              <input
                style={styles.formInput}
                placeholder="RCON Password"
                type="password"
                value={serverForm.password}
                onChange={(e) =>
                  setServerForm({ ...serverForm, password: e.target.value })
                }
                required
              />
            </div>
            <div style={{ display: "flex", gap: "0.5rem" }}>
              <button style={styles.addBtn} type="submit">
                Save Server
              </button>
              <button
                style={{ ...styles.addBtn, background: "#555" }}
                type="button"
                onClick={() => setShowServerForm(false)}
              >
                Cancel
              </button>
            </div>
          </form>
        )}
      </div>

      {/* Members Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Members</h2>
        {memberError && <div style={styles.error}>{memberError}</div>}
        {inviteResult && <div style={styles.success}>{inviteResult}</div>}
        <table style={styles.table}>
          <thead>
            <tr>
              <th style={styles.th}>Name</th>
              <th style={styles.th}>Email</th>
              <th style={styles.th}>Role</th>
              <th style={styles.th}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {members.map((member) => (
              <tr key={member.id}>
                <td style={styles.td}>{member.display_name}</td>
                <td style={styles.td}>{member.email}</td>
                <td style={styles.td}>{member.role}</td>
                <td style={styles.td}>
                  {auth.isOwner && member.role !== "owner" && (
                    <button
                      style={styles.removeBtn}
                      onClick={() => handleRemoveMember(member.id)}
                    >
                      Remove
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {auth.isOwner && !showInviteForm && (
          <button
            style={styles.addBtn}
            onClick={() => setShowInviteForm(true)}
          >
            Invite Member
          </button>
        )}

        {showInviteForm && (
          <form onSubmit={handleInvite} style={{ marginTop: "1rem" }}>
            <div style={styles.formRow}>
              <input
                style={styles.formInput}
                type="email"
                placeholder="Email address"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                required
              />
              <button style={styles.addBtn} type="submit">
                Send Invite
              </button>
              <button
                style={{ ...styles.addBtn, background: "#555" }}
                type="button"
                onClick={() => setShowInviteForm(false)}
              >
                Cancel
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
