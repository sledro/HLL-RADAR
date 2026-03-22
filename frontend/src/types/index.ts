// Player and Match types
export interface PlayerPosition {
  id: number;
  match_id: number;
  player_name: string;
  team: string;
  x: number;
  y: number;
  z: number;
  rotation: number;
  map_name: string;
  timestamp: string; // ISO string format
  platform?: string;
  clan_tag?: string;
  level?: number;
  role?: string;
  unit?: string;
  loadout?: string;
  kills?: number;
  deaths?: number;
  score?: number;
}

export interface Server {
  id: number;
  name: string;
  display_name: string;
  host: string;
  port: number;
  is_active: boolean;
  created_at: string;
}

export interface Match {
  id: number;
  server_id: number;
  map_name: string;
  start_time: string; // ISO string format
  end_time?: string; // ISO string format
  is_active: boolean;
  player_count_peak: number;
  duration_seconds: number;
  final_score_allies: number;
  final_score_axis: number;
}

export interface MatchEvent {
  id: number;
  match_id: number;
  event_type: string;
  message: string;
  details?: string;
  player_ids?: string;
  player_names?: string;
  position_x?: number;
  position_y?: number;
  position_z?: number;
  victim_x?: number;
  victim_y?: number;
  victim_z?: number;
  spawn_type?: string;
  spawn_team?: string;
  spawn_unit?: string;
  spawn_location?: string;
  timestamp: string; // ISO string format
}

// API Response types
export interface MatchData {
  match: Match | null;
  players: PlayerPosition[];
  match_start_time?: string;
  match_end_time?: string;
  duration_seconds: number;
  is_active: boolean;
}

export interface MatchDataResponse {
  match: Match | null;
  players: PlayerPosition[];
  match_start_time?: string;
  match_end_time?: string;
  duration_seconds: number;
  is_active: boolean;
}

export interface MatchListResponse {
  matches: Match[];
}

// WebSocket message types
export interface WebSocketMessage {
  type:
    | "player_update"
    | "player_delta"
    | "match_update"
    | "match_end"
    | "match_start"
    | "match_event"
    | "spawn_event";
  payload: any;
}

export interface PlayerUpdateMessage extends WebSocketMessage {
  type: "player_update";
  payload: {
    players: PlayerPosition[];
    allied_score: number;
    axis_score: number;
    server_id: number;
  };
}

export interface MatchUpdateMessage extends WebSocketMessage {
  type: "match_update";
  payload: {
    match: Match | null;
    is_active: boolean;
  };
}

export interface MatchStartMessage extends WebSocketMessage {
  type: "match_start";
  payload: {
    match: Match;
    is_active: boolean;
  };
}

export interface MatchEndMessage extends WebSocketMessage {
  type: "match_end";
  payload: {
    match_id: number;
    server_id: number;
  };
}

export interface MatchEventMessage extends WebSocketMessage {
  type: "match_event";
  payload: {
    event: MatchEvent;
    server_id: number;
  };
}

// Map configuration
export interface MapConfig {
  name: string;
  displayName: string;
  imageUrl: string;
  imageUrlClean?: string;
  bounds: {
    minX: number;
    maxX: number;
    minY: number;
    maxY: number;
  };
  spawns?: {
    allies: "top" | "bottom" | "left" | "right";
    axis: "top" | "bottom" | "left" | "right";
  };
  strongPoints?: {
    name: string;
    x: number;
    y: number;
    r?: number; // radius as fraction of map size (default 0.035)
  }[];
}

// Frontend state types
export interface HistoricalDataPoint {
  timestamp: number;
  players: PlayerPosition[];
}

export interface PlayerDot {
  id: string;
  element: HTMLElement;
  trail: HTMLElement[];
}

// Kill event from backend (MatchEvent + parsed fields)
export interface KillEvent {
  id: number;
  match_id: number;
  event_type: string;
  message: string;
  details?: string;
  timestamp: string;
  killer_name: string;
  victim_name: string;
  weapon?: string;
  // Killer position (from position_x/y/z)
  position_x?: number;
  position_y?: number;
  position_z?: number;
  // Victim position
  victim_x?: number;
  victim_y?: number;
  victim_z?: number;
}

export interface DeathOverlay {
  player_name: string;
  timestamp: string;
  x: number;
  y: number;
  z: number;
}

export interface SpawnPosition {
  id: number;
  match_id: number;
  player_id: string;
  player_name: string;
  team: string;
  unit: string;
  x: number;
  y: number;
  z: number;
  spawn_type: string;
  timestamp: string;
  confidence: number;
}

export interface SpawnEvent extends MatchEvent {
  spawn_type: string;
  spawn_team: string;
  spawn_unit: string;
  confidence: number;
}

// Multi-tenancy types
export interface AuthStatus {
  mode: string;
  auth_required: boolean;
  authenticated: boolean;
  auth_type: string;
  turnstile_site_key?: string;
}

export interface User {
  id: number;
  email: string;
  display_name: string;
  role: string;
}

export interface Organization {
  id: number;
  name: string;
  slug: string;
}

export interface AuthResponse {
  access_token: string;
  refresh_token: string;
  user: User;
  org: Organization;
}

export interface OrgMember {
  id: number;
  email: string;
  display_name: string;
  role: string;
  created_at: string;
}

export interface InviteResponse {
  id: number;
  email: string;
  invite_url?: string;
  expires_at: string;
}
