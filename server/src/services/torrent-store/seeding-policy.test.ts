import { describe, expect, it } from 'vitest';
import { BITHUMEN_SEED_SECONDS, isEligibleForCleanup } from './seeding-policy';
import type { TorrentResponse } from './types';

const torrent: TorrentResponse = {
  infoHash: 'hash',
  source: 'bithumen',
  name: 'Example',
  progress: 1,
  size: 10,
  downloaded: 10,
  files: [],
  seededSeconds: 0,
};

describe('eight-day BitHUmen seeding policy', () => {
  it('protects BitHUmen until 192 hours even if another source allows deletion', () => {
    for (const seededSeconds of [
      undefined,
      0,
      BITHUMEN_SEED_SECONDS - 1,
      NaN,
      Infinity,
    ]) {
      expect(isEligibleForCleanup({ ...torrent, seededSeconds }, new Set(['hash']))).toBe(
        false,
      );
    }
    expect(
      isEligibleForCleanup(
        { ...torrent, seededSeconds: BITHUMEN_SEED_SECONDS },
        new Set(),
      ),
    ).toBe(true);
  });
  it('allows cleanup of partially downloaded torrents after eight days of sharing', () => {
    expect(
      isEligibleForCleanup(
        { ...torrent, progress: 0.5, seededSeconds: BITHUMEN_SEED_SECONDS },
        new Set(),
      ),
    ).toBe(true);
    expect(
      isEligibleForCleanup(
        { ...torrent, progress: 0.5, seededSeconds: BITHUMEN_SEED_SECONDS - 1 },
        new Set(),
      ),
    ).toBe(false);
  });
  it('preserves nCore tracker-driven deletion', () => {
    expect(isEligibleForCleanup({ ...torrent, source: 'ncore' }, new Set(['hash']))).toBe(
      true,
    );
    expect(
      isEligibleForCleanup(
        { ...torrent, source: 'ncore', seededSeconds: BITHUMEN_SEED_SECONDS },
        new Set(),
      ),
    ).toBe(false);
  });
});
