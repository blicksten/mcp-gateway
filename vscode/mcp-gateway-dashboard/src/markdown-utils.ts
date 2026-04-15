// Escape the markdown-significant characters in a user-supplied string so
// it renders as literal text in a MarkdownString tooltip. A regex character
// class is the only concise way to express "any of this set" — String.split /
// String.replaceAll require one pass per character, and a non-regex loop
// trades conciseness for boilerplate. The set matches CommonMark + GFM
// specials that can trigger formatting inside a MarkdownString body.
const MD_SPECIAL = /[\\`*_{}[\]()#+\-.!|>]/g;

export function escapeMd(s: string): string {
	return s.replace(MD_SPECIAL, (ch) => `\\${ch}`);
}
