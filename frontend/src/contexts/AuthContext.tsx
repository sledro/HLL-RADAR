import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import type { User, Organization } from "../types";
import { apiClient } from "../services/api";

interface AuthContextValue {
  user: User | null;
  org: Organization | null;
  isAuthenticated: boolean;
  isOwner: boolean;
  mode: string;
  isLoading: boolean;
  login: (email: string, password: string) => Promise<void>;
  signup: (
    email: string,
    password: string,
    displayName: string,
    orgName: string
  ) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(() => {
    const stored = localStorage.getItem("user");
    return stored ? JSON.parse(stored) : null;
  });
  const [org, setOrg] = useState<Organization | null>(() => {
    const stored = localStorage.getItem("org");
    return stored ? JSON.parse(stored) : null;
  });
  const [isLoading, setIsLoading] = useState(true);

  const isAuthenticated = !!user && !!localStorage.getItem("access_token");
  const isOwner = user?.role === "owner";

  // Validate token on mount
  useEffect(() => {
    const token = localStorage.getItem("access_token");
    if (!token) {
      setIsLoading(false);
      return;
    }
    // Verify token is still valid by checking auth status
    apiClient
      .getAuthStatus()
      .then((status) => {
        if (!status.authenticated) {
          // Token invalid, clear state
          localStorage.removeItem("access_token");
          localStorage.removeItem("refresh_token");
          localStorage.removeItem("user");
          localStorage.removeItem("org");
          setUser(null);
          setOrg(null);
        }
      })
      .catch(() => {
        // Can't reach backend, keep existing state
      })
      .finally(() => setIsLoading(false));
  }, []);

  // Set up token refresh timer
  useEffect(() => {
    if (!isAuthenticated) return;

    // Refresh token every 10 minutes
    const interval = setInterval(
      async () => {
        const refreshToken = localStorage.getItem("refresh_token");
        if (!refreshToken) return;
        try {
          const result = await apiClient.refreshAccessToken(refreshToken);
          localStorage.setItem("access_token", result.access_token);
          localStorage.setItem("refresh_token", result.refresh_token);
        } catch {
          // Refresh failed, user will be logged out on next 401
        }
      },
      10 * 60 * 1000
    );

    return () => clearInterval(interval);
  }, [isAuthenticated]);

  const login = useCallback(async (email: string, password: string) => {
    const result = await apiClient.login(email, password);
    localStorage.setItem("access_token", result.access_token);
    localStorage.setItem("refresh_token", result.refresh_token);
    localStorage.setItem("user", JSON.stringify(result.user));
    localStorage.setItem("org", JSON.stringify(result.org));
    setUser(result.user);
    setOrg(result.org);
  }, []);

  const signup = useCallback(
    async (
      email: string,
      password: string,
      displayName: string,
      orgName: string
    ) => {
      const result = await apiClient.signup(
        email,
        password,
        displayName,
        orgName
      );
      localStorage.setItem("access_token", result.access_token);
      localStorage.setItem("refresh_token", result.refresh_token);
      localStorage.setItem("user", JSON.stringify(result.user));
      localStorage.setItem("org", JSON.stringify(result.org));
      setUser(result.user);
      setOrg(result.org);
    },
    []
  );

  const logout = useCallback(() => {
    localStorage.removeItem("access_token");
    localStorage.removeItem("refresh_token");
    localStorage.removeItem("user");
    localStorage.removeItem("org");
    setUser(null);
    setOrg(null);
  }, []);

  // Listen for auth-expired events
  useEffect(() => {
    const handler = () => logout();
    window.addEventListener("hll-radar-auth-expired", handler);
    return () => window.removeEventListener("hll-radar-auth-expired", handler);
  }, [logout]);

  return (
    <AuthContext.Provider
      value={{
        user,
        org,
        isAuthenticated,
        isOwner,
        mode: "hosted",
        isLoading,
        login,
        signup,
        logout,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return context;
}
