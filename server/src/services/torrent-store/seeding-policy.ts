import type { TorrentResponse } from './types';

export const BITHUMEN_SEED_SECONDS = 8 * 24 * 60 * 60;

export function isEligibleForCleanup(
  torrent: TorrentResponse,
  sourceCandidates: Set<string>,
): boolean {
  if (torrent.source === 'bithumen') {
    return (
      Number.isFinite(torrent.seededSeconds) &&
      (torrent.seededSeconds ?? 0) >= BITHUMEN_SEED_SECONDS
    );
  }
  return sourceCandidates.has(torrent.infoHash);
}
