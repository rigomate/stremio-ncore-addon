import parseTorrent from 'parse-torrent';
import type { ParsedTorrentDetails } from './types';
import { writeFileWithCreateDir } from '@/utils/files';
import { env } from '@/env';
import { Cached, DEFAULT_TTL } from '@/utils/cache';
import { throwServerError } from '@/utils/errors';

export class TorrentService {
  @Cached({
    max: 1_000,
    ttl: DEFAULT_TTL,
    ttlAutopurge: true,
    generateKey: (torrentUrl, headers = {}) => JSON.stringify([torrentUrl, headers]),
  })
  public async downloadAndParseTorrent(
    torrentUrl: string,
    headers: Record<string, string> = {},
  ): Promise<ParsedTorrentDetails> {
    try {
      const torrentResponse = await fetch(torrentUrl, {
        headers,
        redirect: Object.keys(headers).length ? 'error' : 'follow',
        signal: AbortSignal.timeout(30_000),
      });
      if (!torrentResponse.ok) throw new Error('Torrent download failed');
      const buffer = await torrentResponse.arrayBuffer();
      const torrentData = await parseTorrent(new Uint8Array(buffer));
      return {
        infoHash: torrentData.infoHash,
        files:
          torrentData.files?.map((file) => ({
            name: file.name,
            length: file.length,
            offset: file.offset,
            path: file.path,
          })) ?? [],
      };
    } catch (e) {
      throw throwServerError(e, 'Failed to parse torrent');
    }
  }

  /**
   * @returns the path to the downloaded torrent file
   */
  public async downloadTorrentFile(
    torrentUrl: string,
    headers: Record<string, string> = {},
  ): Promise<string> {
    try {
      const torrentReq = await fetch(torrentUrl, {
        headers,
        redirect: Object.keys(headers).length ? 'error' : 'follow',
        signal: AbortSignal.timeout(30_000),
      });
      if (!torrentReq.ok) throw new Error('Torrent download failed');
      const torrentArrayBuffer = await torrentReq.arrayBuffer();
      const parsedTorrent = await parseTorrent(new Uint8Array(torrentArrayBuffer));
      const torrentFilePath = `${env.TORRENTS_DIR}/${parsedTorrent.infoHash}.torrent`;

      writeFileWithCreateDir(torrentFilePath, Buffer.from(torrentArrayBuffer));
      return torrentFilePath;
    } catch (e) {
      throw throwServerError(e, 'Failed to download torrent file');
    }
  }
}
