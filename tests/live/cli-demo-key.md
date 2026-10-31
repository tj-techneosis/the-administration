# Answer key for examples/cli-demo (kept out of the project)
1. Runs of non-alphanumerics become runs of dashes ("a  b" -> "a--b"); slugs should have single dashes.
2. A leading dash is kept ("  Hello" -> "--hello"); only trailing dashes are trimmed.
3. --file with a missing input file creates an empty output and exits 0; it should fail with a nonzero exit.
4. Truncation at 60 characters can leave a trailing dash (the trim happens before the cut).
5. Blank lines in --file produce empty lines in the output instead of being skipped.
