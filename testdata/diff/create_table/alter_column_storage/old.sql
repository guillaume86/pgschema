CREATE TABLE public.blob_chunk (
    id uuid NOT NULL,
    seq integer NOT NULL,
    bytes bytea NOT NULL,
    note text,
    CONSTRAINT blob_chunk_pkey PRIMARY KEY (id, seq)
);

ALTER TABLE public.blob_chunk ALTER COLUMN note SET STORAGE MAIN;
