export type InfoHash = string;

export interface TorrentStoreStats {
  hash: InfoHash;
  name: string;
  progress: string;
  size: string;
  downloaded: string;
}

export interface TorrentFileResponse {
  name: string;
  path: string;
  size: number;
  progress: number;
}

export interface TorrentResponse {
  source?: string;
  seededSeconds?: number;
  infoHash: InfoHash;
  name: string;
  progress: number;
  size: number;
  downloaded: number;
  files: TorrentFileResponse[];
}

export interface AddTorrentRequest {
  source?: string;
  path: string;
}
