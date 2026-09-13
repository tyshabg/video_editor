export namespace main {
	
	export class ExportOptions {
	    threshold: number;
	    out: string;
	    encode: boolean;
	    height: number;
	    edl: boolean;
	    xml: boolean;
	    no_video: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ExportOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.threshold = source["threshold"];
	        this.out = source["out"];
	        this.encode = source["encode"];
	        this.height = source["height"];
	        this.edl = source["edl"];
	        this.xml = source["xml"];
	        this.no_video = source["no_video"];
	    }
	}
	export class ModelInfo {
	    id: string;
	    input: number;
	    output: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.input = source["input"];
	        this.output = source["output"];
	    }
	}
	export class PlanItem {
	    video: string;
	    start: number;
	    end: number;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new PlanItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video = source["video"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.label = source["label"];
	    }
	}
	export class PlanView {
	    clips: number;
	    duration: number;
	    items: PlanItem[];
	
	    static createFrom(source: any = {}) {
	        return new PlanView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.clips = source["clips"];
	        this.duration = source["duration"];
	        this.items = this.convertValues(source["items"], PlanItem);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProjectSummary {
	    name: string;
	    game: string;
	    videos: number;
	    segments: number;
	    scored: number;
	    duration: number;
	    spent_usd: number;
	    updated: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.game = source["game"];
	        this.videos = source["videos"];
	        this.segments = source["segments"];
	        this.scored = source["scored"];
	        this.duration = source["duration"];
	        this.spent_usd = source["spent_usd"];
	        this.updated = source["updated"];
	    }
	}
	export class SegmentView {
	    id: number;
	    video_id: number;
	    idx: number;
	    start: number;
	    end: number;
	    offset: number;
	    motion_avg: number;
	    cut_score: number;
	    status: string;
	    description: string;
	    score?: number;
	    score_reason: string;
	    score_manual?: number;
	    selected?: boolean;
	    thumbs: string[];
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new SegmentView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.video_id = source["video_id"];
	        this.idx = source["idx"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.offset = source["offset"];
	        this.motion_avg = source["motion_avg"];
	        this.cut_score = source["cut_score"];
	        this.status = source["status"];
	        this.description = source["description"];
	        this.score = source["score"];
	        this.score_reason = source["score_reason"];
	        this.score_manual = source["score_manual"];
	        this.selected = source["selected"];
	        this.thumbs = source["thumbs"];
	        this.error = source["error"];
	    }
	}
	export class VideoView {
	    id: number;
	    name: string;
	    path: string;
	    duration: number;
	    offset: number;
	    width: number;
	    height: number;
	    fps: number;
	    analyzed: boolean;
	    segments: number;
	    recorded_at: string;
	
	    static createFrom(source: any = {}) {
	        return new VideoView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.duration = source["duration"];
	        this.offset = source["offset"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	        this.analyzed = source["analyzed"];
	        this.segments = source["segments"];
	        this.recorded_at = source["recorded_at"];
	    }
	}
	export class ProjectView {
	    name: string;
	    game: string;
	    videos: VideoView[];
	    segments: SegmentView[];
	    duration: number;
	    usage: Record<string, store.Usage>;
	    estimate: pipeline.Estimate;
	
	    static createFrom(source: any = {}) {
	        return new ProjectView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.game = source["game"];
	        this.videos = this.convertValues(source["videos"], VideoView);
	        this.segments = this.convertValues(source["segments"], SegmentView);
	        this.duration = source["duration"];
	        this.usage = this.convertValues(source["usage"], store.Usage, true);
	        this.estimate = this.convertValues(source["estimate"], pipeline.Estimate);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class SettingsView {
	    data_dir: string;
	    data_dir_source: string;
	    data_dir_env: boolean;
	    config_path: string;
	    ffmpeg_path: string;
	    ffmpeg_version: string;
	    ffmpeg_error: string;
	    api_key_set: boolean;
	    api_key_hint: string;
	    api_key_source: string;
	    model: string;
	    lang: string;
	    running: boolean;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.data_dir = source["data_dir"];
	        this.data_dir_source = source["data_dir_source"];
	        this.data_dir_env = source["data_dir_env"];
	        this.config_path = source["config_path"];
	        this.ffmpeg_path = source["ffmpeg_path"];
	        this.ffmpeg_version = source["ffmpeg_version"];
	        this.ffmpeg_error = source["ffmpeg_error"];
	        this.api_key_set = source["api_key_set"];
	        this.api_key_hint = source["api_key_hint"];
	        this.api_key_source = source["api_key_source"];
	        this.model = source["model"];
	        this.lang = source["lang"];
	        this.running = source["running"];
	        this.version = source["version"];
	    }
	}

}

export namespace pipeline {
	
	export class Estimate {
	    model: string;
	    segments: number;
	    to_describe: number;
	    to_score: number;
	    describe_usd: number;
	    score_usd: number;
	    remaining_usd: number;
	    spent_usd: number;
	    spent_calls: number;
	    spent_input_tokens: number;
	    unknown_pricing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Estimate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.segments = source["segments"];
	        this.to_describe = source["to_describe"];
	        this.to_score = source["to_score"];
	        this.describe_usd = source["describe_usd"];
	        this.score_usd = source["score_usd"];
	        this.remaining_usd = source["remaining_usd"];
	        this.spent_usd = source["spent_usd"];
	        this.spent_calls = source["spent_calls"];
	        this.spent_input_tokens = source["spent_input_tokens"];
	        this.unknown_pricing = source["unknown_pricing"];
	    }
	}

}

export namespace store {
	
	export class Usage {
	    calls: number;
	    input_tokens: number;
	    output_tokens: number;
	    cache_read: number;
	    cost_usd: number;
	
	    static createFrom(source: any = {}) {
	        return new Usage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.calls = source["calls"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cache_read = source["cache_read"];
	        this.cost_usd = source["cost_usd"];
	    }
	}

}

