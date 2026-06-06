export interface Identity {
  user_id: string;
  tenant_id: string;
  auth_method: string;
  scopes: string[];
}

export interface LoginResponse extends Identity {
  csrf_token: string;
}

export interface Connection {
  provider: string;
  status: string;
  connected: boolean;
  scopes: string[];
}

export interface Project {
  key: string;
  name: string;
}

export interface Ticket {
  issue_key: string;
  title: string;
  url: string;
  project_key: string;
  created_at: string;
}

export interface CreatedTicket {
  provider: string;
  issue_key: string;
  url: string;
}

export interface ApiToken {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  expires_at?: string;
  last_used_at?: string;
  revoked_at?: string;
  created_at: string;
}

export interface IssuedToken extends ApiToken {
  token: string;
}

export interface Automation {
  id: string;
  name: string;
  site_url: string;
  provider: string;
  project_key: string;
  interval_seconds: number;
  enabled: boolean;
  status: string;
  next_scan_at: string;
  last_run_at?: string;
  last_error?: string;
  created_at: string;
}

// ApiError carries the structured error envelope from the backend.
export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}
