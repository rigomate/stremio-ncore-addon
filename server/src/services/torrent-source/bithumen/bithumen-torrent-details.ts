import { TorrentDetails, type ParsedTorrentDetails } from '../types';
import { Language, Resolution } from '@/db/schema/users';

export interface BithumenResult {
  id: string;
  title: string;
  category: number;
  seeders: number;
}

export class BithumenTorrentDetails extends TorrentDetails {
  public sourceName = 'bithumen';
  public sourceId: string;
  public infoHash: string;
  public files: ParsedTorrentDetails['files'];
  public fallbackResolution: Resolution;
  public isSpeculated = false;

  constructor(
    private result: BithumenResult,
    parsed: ParsedTorrentDetails,
  ) {
    super();
    this.sourceId = result.id;
    this.infoHash = parsed.infoHash;
    this.files = parsed.files;
    this.fallbackResolution = [19, 23, 7, 26, 20, 24].includes(result.category)
      ? Resolution.R480P
      : [37, 39, 33, 40].includes(result.category)
        ? Resolution.R1080P
        : Resolution.R720P;
  }

  public displayResolution(resolution: Resolution): string {
    return `BitHUmen (${resolution})`;
  }

  public getName(): string {
    return this.result.title;
  }
  public getSeeders(): number {
    return this.result.seeders;
  }
  public getLanguage(): Language {
    return [23, 24, 25, 37, 33, 7, 41].includes(this.result.category)
      ? Language.HU
      : Language.EN;
  }
}
