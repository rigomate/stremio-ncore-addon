import { JSDOM } from 'jsdom';
import { LRUCache } from 'lru-cache';
import type { TorrentSource } from '../types';
import { BithumenTorrentDetails, type BithumenResult } from './bithumen-torrent-details';
import type { TorrentService } from '@/services/torrent';
import type { CinemeatService } from '@/services/cinemeta';
import { StreamType, type StreamQuery } from '@/schemas/stream.schema';
import { isSupportedMedia } from '@/utils/media-file-extensions';
import { DEFAULT_TTL } from '@/utils/cache';

const MOVIE_CATEGORIES = [23, 24, 25, 37, 33, 19, 20, 5, 39, 40];
const SERIES_CATEGORIES = [7, 41, 26, 42];

// Protocol and selectors verified against Jackett's BitHUmen definition:
// https://github.com/Jackett/Jackett/blob/master/src/Jackett.Common/Definitions/bithumen.yml
export class BithumenService implements TorrentSource {
  public name = 'bithumen';
  public displayName = 'BitHUmen';
  private cache = new LRUCache<string, BithumenTorrentDetails[]>({
    max: 100,
    ttl: DEFAULT_TTL,
  });
  private baseUrl: URL;

  constructor(
    private torrentService: TorrentService,
    private cinemetaService: CinemeatService,
    baseUrl: string,
    private cookie: string,
  ) {
    this.baseUrl = new URL(baseUrl);
  }

  public getDownloadHeaders(): Record<string, string> {
    return { Cookie: this.cookie };
  }

  private async getDocument(url: URL): Promise<Document> {
    const response = await fetch(url, {
      headers: this.getDownloadHeaders(),
      redirect: 'manual',
      signal: AbortSignal.timeout(30_000),
    });
    if (!response.ok) throw new Error('BitHUmen request failed. Check BITHUMEN_COOKIE.');
    const html = new TextDecoder('iso-8859-2').decode(await response.arrayBuffer());
    const document = new JSDOM(html).window.document;
    if (!document.querySelector('a[href*="logout.php?"]')) {
      throw new Error(
        'BitHUmen session expired or page unavailable. Update BITHUMEN_COOKIE.',
      );
    }
    return document;
  }

  public async getConfigIssues(): Promise<string | null> {
    try {
      await this.getDocument(new URL('/index.php', this.baseUrl));
      return null;
    } catch {
      return 'Could not access BitHUmen. Check BITHUMEN_URL and replace BITHUMEN_COOKIE with a valid browser session cookie.';
    }
  }

  public async getTorrentUrlBySourceId(sourceId: string): Promise<string | null> {
    if (!/^\d+$/.test(sourceId)) return null;
    return new URL(`/download.php/${sourceId}/invalid.torrent`, this.baseUrl).href;
  }

  public async getRemovableInfoHashes(): Promise<string[]> {
    // BitHUmen cleanup uses eight days of local seeding time in TorrentStoreService.
    return [];
  }

  private async search(
    query: string,
    type: StreamType,
    byTitle = false,
  ): Promise<BithumenTorrentDetails[]> {
    const cacheKey = JSON.stringify([query, type, byTitle]);
    const cached = this.cache.get(cacheKey);
    if (cached) return cached;
    const categories = type === StreamType.MOVIE ? MOVIE_CATEGORIES : SERIES_CATEGORIES;
    const url = new URL('/browse.php', this.baseUrl);
    url.searchParams.set('search', query);
    url.searchParams.set('onlytitle', byTitle ? 'yes' : '');
    url.searchParams.set('sort', 'seeders');
    url.searchParams.set('d', 'DESC');
    categories.forEach((category) => url.searchParams.set(`c${category}`, '1'));
    const pending = [url];
    const visited = new Set<string>();
    const results = new Map<string, BithumenResult>();
    // Traverse pagination with a finite cap to avoid crawling unbounded tracker pages.
    while (pending.length && visited.size < 20) {
      const page = pending.shift()!;
      if (visited.has(page.href)) continue;
      visited.add(page.href);
      const document = await this.getDocument(page);
      for (const row of document.querySelectorAll('table#torrenttable > tbody > tr')) {
        const link = row.querySelector('a[href^="details.php?id="]');
        if (!link) continue;
        const id = new URL(link.getAttribute('href')!, this.baseUrl).searchParams.get(
          'id',
        );
        const categoryLink = row.querySelector('a[href^="?cat="]');
        const category = Number(
          new URL(
            categoryLink?.getAttribute('href') ?? '',
            this.baseUrl,
          ).searchParams.get('cat'),
        );
        const title = (link.getAttribute('title') || link.textContent || '').trim();
        if (!id || !/^\d+$/.test(id) || !title || !categories.includes(category))
          continue;
        results.set(id, {
          id,
          title,
          category,
          seeders: Number(
            row.querySelector('td:nth-child(8)')?.textContent?.replace(/[^\d]/g, '') || 0,
          ),
        });
      }
      for (const link of document.querySelectorAll('a[href]')) {
        const next = new URL(link.getAttribute('href')!, page);
        const pageNumber = next.searchParams.get('page');
        if (
          next.origin !== this.baseUrl.origin ||
          next.pathname !== '/browse.php' ||
          !pageNumber ||
          !/^\d+$/.test(pageNumber)
        )
          continue;
        const nextPage = new URL(url);
        nextPage.searchParams.set('page', pageNumber);
        if (
          !visited.has(nextPage.href) &&
          !pending.some((item) => item.href === nextPage.href)
        )
          pending.push(nextPage);
      }
    }
    const torrents: BithumenTorrentDetails[] = [];
    for (const result of results.values()) {
      try {
        const downloadUrl = (await this.getTorrentUrlBySourceId(result.id))!;
        const parsed = await this.torrentService.downloadAndParseTorrent(
          downloadUrl,
          this.getDownloadHeaders(),
        );
        const torrent = new BithumenTorrentDetails(result, parsed);
        torrent.isSpeculated = byTitle;
        torrents.push(torrent);
      } catch {
        console.error(
          `Failed to download BitHUmen torrent ${result.id}. Check the tracker session.`,
        );
      }
    }
    // Retry failures on the next request instead of caching a failed download as an empty search.
    if (torrents.length === results.size) this.cache.set(cacheKey, torrents);
    return torrents;
  }

  public async getTorrentsForImdbId({
    imdbId,
    type,
    season,
    episode,
  }: Pick<StreamQuery, 'imdbId' | 'type' | 'season' | 'episode'>): Promise<
    BithumenTorrentDetails[]
  > {
    const filter = (torrents: BithumenTorrentDetails[]) =>
      torrents.filter((torrent) => {
        const file = torrent.files[torrent.getMediaFileIndex({ season, episode })];
        return file !== undefined && isSupportedMedia(file.path);
      });
    const matches = filter(await this.search(imdbId, type));
    if (matches.length) return matches;
    const metadata = await this.cinemetaService.getMetadataByImdbId(type, imdbId);
    return filter(await this.search(metadata.meta.name, type, true));
  }
}
