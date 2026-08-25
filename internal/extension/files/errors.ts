/**
 * Error types shared by the extension.
 *
 * ADR 0001: while remote mode is active, project-facing tools must fail
 * closed. Loss of the daemon or workspace must produce a visible error and
 * must never cause silent fallback to the developer machine.
 */

/** The workspace is not usable (disconnected, misconfigured, or missing). */
export class ScrapsUnavailableError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "ScrapsUnavailableError";
	}
}

/** scrapd answered with a non-2xx response. */
export class ScrapdApiError extends Error {
	readonly status: number;
	/** Machine-readable error code (ADR 0002), when the body carried one. */
	readonly code: string | undefined;

	constructor(status: number, message: string, code?: string) {
		super(message.length > 0 ? message : `scrapd request failed with status ${status}`);
		this.name = "ScrapdApiError";
		this.status = status;
		this.code = code;
	}

	/** Build an error from a fetch Response, preferring a JSON error body. */
	static async from(response: Response): Promise<ScrapdApiError> {
		let message = "";
		let code: string | undefined;
		try {
			const body = (await response.json()) as {
				error?: { code?: string; message?: string } | string;
				message?: string;
			};
			if (typeof body.error === "string") {
				message = body.error;
			} else if (body.error !== undefined) {
				message = body.error.message ?? "";
				code = body.error.code;
			} else {
				message = body.message ?? "";
			}
		} catch {
			// Non-JSON error body; fall back to the status code.
		}
		return new ScrapdApiError(response.status, message, code);
	}
}
