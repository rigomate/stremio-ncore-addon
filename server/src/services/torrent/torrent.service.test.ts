import { afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const directory = mkdtempSync(join(tmpdir(), 'addon-torrent-test-'));
vi.mock('@/env', () => ({ env: { TORRENTS_DIR: directory } }));
const { TorrentService } = await import('./torrent.service');
const bytes = Buffer.from(
  'd4:infod6:lengthi1e4:name8:test.mkv12:piece lengthi16384e6:pieces20:12345678901234567890ee',
);

afterEach(() => vi.unstubAllGlobals());

describe('authenticated torrent downloads', () => {
  it('sends cookies for parsing and playback and saves valid torrent bytes without a filename header', async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(() => Promise.resolve(new Response(bytes)));
    vi.stubGlobal('fetch', fetcher);
    const service = new TorrentService();
    const headers = { Cookie: 'uid=123' };
    const parsed = await service.downloadAndParseTorrent(
      'https://bithumen.be/download.php/1/invalid.torrent',
      headers,
    );
    expect(parsed.files[0].name).toBe('test.mkv');
    try {
      const path = await service.downloadTorrentFile(
        'https://bithumen.be/download.php/1/invalid.torrent',
        headers,
      );
      expect(readFileSync(path)).toEqual(bytes);
      expect(fetcher).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ headers, redirect: 'error' }),
      );
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  });
  it('rejects an unsuccessful torrent response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('Unauthorized', { status: 401 })),
    );
    await expect(
      new TorrentService().downloadAndParseTorrent(
        'https://bithumen.be/download.php/2/invalid.torrent',
        { Cookie: 'expired=1' },
      ),
    ).rejects.toThrow();
  });
});
