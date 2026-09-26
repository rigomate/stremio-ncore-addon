import { afterEach, describe, expect, it, vi } from 'vitest';
import { BithumenService } from './bithumen.service';
import { TorrentSourceManager } from '../torrent-source-manager';
import { StreamType } from '@/schemas/stream.schema';
import { Language } from '@/db/schema/users';
import type { TorrentService } from '@/services/torrent';
import type { CinemeatService } from '@/services/cinemeta';

const logout = '<a href="/logout.php?token=1">Logout</a>';
const row = (id: string, category = 37) =>
  `<tr><td><a href="?cat=${category}">category</a></td><td><a href="details.php?id=${id}" title="Example.1080p">Example</a></td><td></td><td></td><td></td><td></td><td></td><td>1,234</td></tr>`;
const page = (rows = '', extra = '') =>
  new Response(
    `${logout}<table id="torrenttable"><tbody>${rows}</tbody></table>${extra}`,
  );
const parsed = {
  infoHash: 'hash',
  files: [
    { name: 'Example.S01E02.mkv', path: 'Example.S01E02.mkv', length: 100, offset: 0 },
  ],
};
const setup = () => {
  const download = vi.fn().mockResolvedValue(parsed);
  const metadata = vi.fn().mockResolvedValue({ meta: { name: 'Example' } });
  const service = new BithumenService(
    { downloadAndParseTorrent: download } as unknown as TorrentService,
    { getMetadataByImdbId: metadata } as unknown as CinemeatService,
    'https://bithumen.be',
    'uid=123; pass=secret',
  );
  return { service, download, metadata };
};
afterEach(() => vi.unstubAllGlobals());

describe('BitHUmen provider', () => {
  it('searches IMDb, follows pagination, deduplicates and downloads using cookies', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(page(row('1'), '<a href="browse.php?page=1">next</a>'))
      .mockResolvedValueOnce(page(row('1') + row('2')));
    vi.stubGlobal('fetch', fetcher);
    const { service, download } = setup();
    const result = await service.getTorrentsForImdbId({
      imdbId: 'tt1234567',
      type: StreamType.MOVIE,
    });
    expect(result).toHaveLength(2);
    expect(result[0].getSeeders()).toBe(1234);
    expect(result[0].getLanguage()).toBe(Language.HU);
    expect(result[0].sourceName).toBe('bithumen');
    expect(fetcher.mock.calls[0][0].searchParams.get('search')).toBe('tt1234567');
    expect(download).toHaveBeenCalledWith(
      'https://bithumen.be/download.php/1/invalid.torrent',
      { Cookie: 'uid=123; pass=secret' },
    );
    expect(new TorrentSourceManager([service]).getDownloadHeaders('bithumen')).toEqual({
      Cookie: 'uid=123; pass=secret',
    });
  });
  it('falls back to title search and selects the requested episode', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(page())
        .mockResolvedValueOnce(page(row('3', 41))),
    );
    const { service } = setup();
    const result = await service.getTorrentsForImdbId({
      imdbId: 'tt1234567',
      type: StreamType.TV_SHOW,
      season: 1,
      episode: 2,
    });
    expect(result).toHaveLength(1);
    expect(result[0].isSpeculated).toBe(true);
    expect(result[0].getMediaFileIndex({ season: 1, episode: 3 })).toBe(-1);
  });
  it('reports expired cookies, rejects invalid source IDs and never marks torrents removable', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockImplementation(() => Promise.resolve(new Response('<form>Login</form>'))),
    );
    const { service } = setup();
    expect(await service.getConfigIssues()).toContain('BITHUMEN_COOKIE');
    expect(await service.getTorrentUrlBySourceId('../evil')).toBeNull();
    expect(await service.getRemovableInfoHashes()).toEqual([]);
    await expect(
      service.getTorrentsForImdbId({ imdbId: 'tt1234567', type: StreamType.MOVIE }),
    ).rejects.toThrow('session expired');
  });
  it('keeps another tracker available when BitHUmen fails', async () => {
    const { service } = setup();
    vi.spyOn(service, 'getTorrentsForImdbId').mockRejectedValue(new Error('unavailable'));
    const other = setup().service;
    vi.spyOn(other, 'getTorrentsForImdbId').mockResolvedValue([]);
    const log = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(
      await new TorrentSourceManager([service, other]).getTorrentsForImdbId({
        imdbId: 'tt1234567',
        type: StreamType.MOVIE,
      }),
    ).toEqual([]);
    expect(other.getTorrentsForImdbId).toHaveBeenCalled();
    log.mockRestore();
  });
});
