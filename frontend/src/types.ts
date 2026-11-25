export type Page =
	| "home"
  | "overview"
  | "egress-dashboard"
  | "proxy-dashboard"
  | "users"
  | "activity"
  | "egress"
  | "egress-records"
  | "settings";
export type Session = {
  authenticated: boolean;
  username?: string;
  csrf?: string;
};
export type Limits = {
  total_bytes: number;
  daily_bytes: number;
  speed_bps: number;
  total_used: number;
  daily_used: number;
  blocked: "total" | "daily" | null;
  ready?: boolean;
};
export type User = {
  deleted?: boolean;
  deleted_at?: number | null;
  id: number;
  name: string;
  enabled: boolean | number;
  created: number;
  last_used: number | null;
  upload: number;
  download: number;
  domains: number;
  connections: number;
  limits?: Limits;
  clash_url: string;
  shadowrocket_url: string;
};
export type Point = { ts: number; upload: number; download: number };
export type Domain = {
  domain: string;
  connections: number;
  upload?: number;
  download?: number;
};
export type Rates = {
  speed_upload: number | null;
  speed_download: number | null;
  collector?: { last_poll: number | null; last_error: string | null };
  live_devices?: number;
  online_devices?: number;
};
export type Trend = { year: string; month: string; day: string };
export type ProxyOverview = Rates & {
  upload: number;
  download: number;
  day_upload: number;
  day_download: number;
  active: number;
  users: number;
  connections: number;
  series: Point[];
  domains: Domain[];
  trend: { granularity: "hour" | "day" | "month"; retained_since: number };
};
export type Device = {
  id: string;
  name: string;
  online: boolean;
  enabled: boolean;
  deleted?: boolean;
  paired: boolean;
  browser: {
    platformName?: string;
    platform?: string;
    platformVersion?: string;
  };
  last_ip: string | null;
  last_seen: number | null;
  driver_version: string | null;
};
export type DeviceUsage = Pick<
  Device,
  "id" | "name" | "online" | "enabled" | "deleted" | "driver_version"
> & {
  upload: number;
  download: number;
  connections: number;
  succeeded: number;
  last_record: number | null;
};
export type DeviceOption = { id: string; name: string; deleted: boolean };
export type EgressOverview = Rates & {
  total_upload: number;
  total_download: number;
  day_upload: number;
  day_download: number;
  enabled: number;
  available: number;
  day_connections: number;
  day_succeeded: number;
  online: number;
  hours: number;
  since: number;
  until: number;
  connections: number;
  series: Point[];
  domains: Domain[];
  devices: DeviceUsage[];
  device_options: DeviceOption[];
};
export type BlacklistRule = {
  id: number;
  host: string;
  enabled: boolean;
  updated: number;
};
export type RecordItem = {
  id: number;
  ts: number;
  name: string;
  domain: string;
  port: number;
  network?: string;
  kind?: string;
  action?: string;
  method?: string;
  outcome?: string;
  upload?: number;
  download?: number;
  duration_ms?: number;
  exit_ip?: string;
  detail?: string;
};
export type RecordPage = {
  items: RecordItem[];
  total: number;
  pages: number;
  page: number;
  page_size: number;
  before_id: number | null;
};
export type RecordScope = {
  device_id: string;
  hours: string;
  start?: number;
  key: number;
};
export type Resources = {
  cpu_percent: number | null;
  cpu_cores: number;
  load_average: number[] | null;
  memory: { used: number; total: number; percent: number } | null;
  disk: { used: number; total: number; percent: number } | null;
  uptime_seconds: number | null;
  sampled_at: number;
};
export type Service = {
  name: string;
  state: string;
  pid: number;
  memory: number | null;
  working_directory?: string;
};
export type Server = {
  hostname: string;
  os: string;
  kernel: string;
  architecture: string;
  cpu_model: string;
  timezone: string;
  domain: string;
  protocol: string;
  versions: { go?: string };
  python?: string;
  processes: number;
  database_bytes: number | null;
  services: Service[];
};
