--
-- PostgreSQL database dump
--

\restrict fhhChmVPkfpO10dHQRM6x5ayUpXr2YY9wKj2tdohAXjdLOF13Lpg2VGMxp7JU8H

-- Dumped from database version 17.6
-- Dumped by pg_dump version 17.6

-- Started on 2025-09-29 23:20:52

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- TOC entry 230 (class 1259 OID 24769)
-- Name: metadata_types; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.metadata_types (
    id bigint NOT NULL,
    name character varying(255) NOT NULL,
    description character varying(1024)
);


ALTER TABLE public.metadata_types OWNER TO postgres;

--
-- TOC entry 229 (class 1259 OID 24768)
-- Name: metadata_types_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.metadata_types_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.metadata_types_id_seq OWNER TO postgres;

--
-- TOC entry 4911 (class 0 OID 0)
-- Dependencies: 229
-- Name: metadata_types_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.metadata_types_id_seq OWNED BY public.metadata_types.id;


--
-- TOC entry 220 (class 1259 OID 24587)
-- Name: segments; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.segments (
    id bigint NOT NULL,
    station_id integer,
    segment_name character varying(255) NOT NULL,
    order_num integer NOT NULL
);


ALTER TABLE public.segments OWNER TO postgres;

--
-- TOC entry 219 (class 1259 OID 24586)
-- Name: segments_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.segments_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.segments_id_seq OWNER TO postgres;

--
-- TOC entry 4912 (class 0 OID 0)
-- Dependencies: 219
-- Name: segments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.segments_id_seq OWNED BY public.segments.id;


--
-- TOC entry 236 (class 1259 OID 25122)
-- Name: station_videos_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.station_videos_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.station_videos_id_seq OWNER TO postgres;

--
-- TOC entry 235 (class 1259 OID 25113)
-- Name: station_videos; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.station_videos (
    id bigint DEFAULT nextval('public.station_videos_id_seq'::regclass) NOT NULL,
    station_id bigint NOT NULL,
    video_id bigint NOT NULL
);


ALTER TABLE public.station_videos OWNER TO postgres;

--
-- TOC entry 218 (class 1259 OID 24578)
-- Name: stations; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.stations (
    id bigint NOT NULL,
    name character varying(255) NOT NULL,
    unix_start bigint DEFAULT 0 NOT NULL
);


ALTER TABLE public.stations OWNER TO postgres;

--
-- TOC entry 217 (class 1259 OID 24577)
-- Name: stations_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.stations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.stations_id_seq OWNER TO postgres;

--
-- TOC entry 4913 (class 0 OID 0)
-- Dependencies: 217
-- Name: stations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.stations_id_seq OWNED BY public.stations.id;


--
-- TOC entry 226 (class 1259 OID 24712)
-- Name: tags; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.tags (
    id bigint NOT NULL,
    name character varying(255) NOT NULL
);


ALTER TABLE public.tags OWNER TO postgres;

--
-- TOC entry 225 (class 1259 OID 24711)
-- Name: tags_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.tags_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.tags_id_seq OWNER TO postgres;

--
-- TOC entry 4914 (class 0 OID 0)
-- Dependencies: 225
-- Name: tags_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.tags_id_seq OWNED BY public.tags.id;


--
-- TOC entry 234 (class 1259 OID 24866)
-- Name: title_metadata; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.title_metadata (
    id bigint NOT NULL,
    title_id bigint NOT NULL,
    metadata_type_id bigint NOT NULL,
    value jsonb NOT NULL
);


ALTER TABLE public.title_metadata OWNER TO postgres;

--
-- TOC entry 233 (class 1259 OID 24865)
-- Name: title_metadata_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

ALTER TABLE public.title_metadata ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.title_metadata_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- TOC entry 222 (class 1259 OID 24686)
-- Name: titles; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.titles (
    id bigint NOT NULL,
    name character varying(255) NOT NULL,
    description text
);


ALTER TABLE public.titles OWNER TO postgres;

--
-- TOC entry 221 (class 1259 OID 24685)
-- Name: titles_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.titles_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.titles_id_seq OWNER TO postgres;

--
-- TOC entry 4915 (class 0 OID 0)
-- Dependencies: 221
-- Name: titles_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.titles_id_seq OWNED BY public.titles.id;


--
-- TOC entry 232 (class 1259 OID 24778)
-- Name: video_metadata; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.video_metadata (
    id bigint NOT NULL,
    video_id bigint NOT NULL,
    metadata_type_id bigint NOT NULL,
    value jsonb NOT NULL
);


ALTER TABLE public.video_metadata OWNER TO postgres;

--
-- TOC entry 231 (class 1259 OID 24777)
-- Name: video_metadata_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.video_metadata_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.video_metadata_id_seq OWNER TO postgres;

--
-- TOC entry 4916 (class 0 OID 0)
-- Dependencies: 231
-- Name: video_metadata_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.video_metadata_id_seq OWNED BY public.video_metadata.id;


--
-- TOC entry 228 (class 1259 OID 24721)
-- Name: video_tags; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.video_tags (
    id bigint NOT NULL,
    video_id bigint NOT NULL,
    tag_id bigint NOT NULL
);


ALTER TABLE public.video_tags OWNER TO postgres;

--
-- TOC entry 227 (class 1259 OID 24720)
-- Name: video_tags_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.video_tags_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.video_tags_id_seq OWNER TO postgres;

--
-- TOC entry 4917 (class 0 OID 0)
-- Dependencies: 227
-- Name: video_tags_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.video_tags_id_seq OWNED BY public.video_tags.id;


--
-- TOC entry 224 (class 1259 OID 24696)
-- Name: videos; Type: TABLE; Schema: public; Owner: postgres
--

CREATE TABLE public.videos (
    id bigint NOT NULL,
    title_id bigint NOT NULL,
    uri text NOT NULL,
    duration numeric(18,12)
);


ALTER TABLE public.videos OWNER TO postgres;

--
-- TOC entry 223 (class 1259 OID 24695)
-- Name: videos_id_seq; Type: SEQUENCE; Schema: public; Owner: postgres
--

CREATE SEQUENCE public.videos_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.videos_id_seq OWNER TO postgres;

--
-- TOC entry 4918 (class 0 OID 0)
-- Dependencies: 223
-- Name: videos_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: postgres
--

ALTER SEQUENCE public.videos_id_seq OWNED BY public.videos.id;


--
-- TOC entry 4693 (class 2604 OID 24772)
-- Name: metadata_types id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.metadata_types ALTER COLUMN id SET DEFAULT nextval('public.metadata_types_id_seq'::regclass);


--
-- TOC entry 4688 (class 2604 OID 24809)
-- Name: segments id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.segments ALTER COLUMN id SET DEFAULT nextval('public.segments_id_seq'::regclass);


--
-- TOC entry 4686 (class 2604 OID 24633)
-- Name: stations id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.stations ALTER COLUMN id SET DEFAULT nextval('public.stations_id_seq'::regclass);


--
-- TOC entry 4691 (class 2604 OID 24715)
-- Name: tags id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.tags ALTER COLUMN id SET DEFAULT nextval('public.tags_id_seq'::regclass);


--
-- TOC entry 4689 (class 2604 OID 24689)
-- Name: titles id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.titles ALTER COLUMN id SET DEFAULT nextval('public.titles_id_seq'::regclass);


--
-- TOC entry 4694 (class 2604 OID 24781)
-- Name: video_metadata id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_metadata ALTER COLUMN id SET DEFAULT nextval('public.video_metadata_id_seq'::regclass);


--
-- TOC entry 4692 (class 2604 OID 24724)
-- Name: video_tags id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_tags ALTER COLUMN id SET DEFAULT nextval('public.video_tags_id_seq'::regclass);


--
-- TOC entry 4690 (class 2604 OID 24699)
-- Name: videos id; Type: DEFAULT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.videos ALTER COLUMN id SET DEFAULT nextval('public.videos_id_seq'::regclass);


--
-- TOC entry 4899 (class 0 OID 24769)
-- Dependencies: 230
-- Data for Name: metadata_types; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.metadata_types (id, name, description) FROM stdin;
1	break_point	The point in a video where a quiet blank screen transition occurs. Useful for determining where ads should be inserted.
\.


--
-- TOC entry 4889 (class 0 OID 24587)
-- Dependencies: 220
-- Data for Name: segments; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.segments (id, station_id, segment_name, order_num) FROM stdin;
1	1	3rd_Rock_from_the_Sun_ad_2.h264	1
2	1	3rd_Rock_from_the_Sun_ad_3.h264	2
3	1	3rd_Rock_from_the_Sun_seg_2.h264	3
4	1	3rd_Rock_from_the_Sun_ad_4.h264	4
5	1	3rd_Rock_from_the_Sun_ad_5.h264	5
6	1	3rd_Rock_from_the_Sun_ad_6.h264	6
7	1	3rd_Rock_from_the_Sun_seg_3.h264	7
8	1	3rd_Rock_from_the_Sun_ad_7.h264	8
9	1	3rd_Rock_from_the_Sun_ad_8.h264	9
10	1	3rd_Rock_from_the_Sun_ad_9.h264	10
11	1	3rd_Rock_from_the_Sun_seg_4.h264	11
12	1	3rd_Rock_from_the_Sun_ad_10.h264	12
13	1	3rd_Rock_from_the_Sun_ad_11.h264	13
14	1	3rd_Rock_from_the_Sun_ad_12.h264	14
15	1	3rd_Rock_from_the_Sun_seg_5.h264	15
16	2	Bernie_Mac_seg_1.h264	16
17	2	Bernie_Mac_ad_1.h264	17
18	2	Bernie_Mac_ad_2.h264	18
19	2	Bernie_Mac_ad_3.h264	19
20	2	Bernie_Mac_seg_2.h264	20
21	2	Bernie_Mac_ad_4.h264	21
22	2	Bernie_Mac_ad_5.h264	22
23	2	Bernie_Mac_ad_6.h264	23
24	2	Bernie_Mac_seg_3.h264	24
\.


--
-- TOC entry 4904 (class 0 OID 25113)
-- Dependencies: 235
-- Data for Name: station_videos; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.station_videos (id, station_id, video_id) FROM stdin;
101	3	154
102	3	155
103	3	156
104	3	157
7	3	7
61	3	114
62	3	115
63	3	116
64	3	117
65	3	118
66	3	119
67	3	120
105	3	158
68	3	121
69	3	122
70	3	123
71	3	124
1	3	1
2	3	2
3	3	3
4	3	4
5	3	5
6	3	6
8	3	8
9	3	9
10	3	10
11	3	11
12	3	12
13	3	13
106	3	159
107	3	160
108	3	161
109	3	162
110	3	163
111	3	164
112	3	165
113	3	166
114	3	167
115	3	168
116	3	169
117	3	170
118	3	171
119	3	172
120	3	173
121	3	174
122	3	175
123	3	176
124	3	177
125	3	178
126	3	179
127	3	180
128	3	181
129	3	182
130	3	183
131	3	184
132	3	185
133	3	186
134	3	187
135	3	188
136	3	189
137	3	190
138	3	191
139	3	192
140	3	193
141	3	194
142	3	195
143	3	196
144	3	197
145	3	198
146	3	199
147	3	200
148	3	201
149	3	202
14	3	67
15	3	68
16	3	69
17	3	70
18	3	71
19	3	72
20	3	73
21	3	74
22	3	75
23	3	76
24	3	77
25	3	78
26	3	79
27	3	80
28	3	81
29	3	82
30	3	83
31	3	84
32	3	85
33	3	86
34	3	87
35	3	88
36	3	89
37	3	90
38	3	91
39	3	92
40	3	93
41	3	94
42	3	95
43	3	96
44	3	97
45	3	98
46	3	99
47	3	100
48	3	101
49	3	102
50	3	103
51	3	104
52	3	105
53	3	106
54	3	107
55	3	108
56	3	109
57	3	110
58	3	111
59	3	112
60	3	113
72	3	125
73	3	126
74	3	127
75	3	128
76	3	129
77	3	130
78	3	131
79	3	132
80	3	133
81	3	134
82	3	135
83	3	136
84	3	137
85	3	138
86	3	139
87	3	140
88	3	141
89	3	142
90	3	143
91	3	144
92	3	145
93	3	146
94	3	147
95	3	148
96	3	149
97	3	150
98	3	151
99	3	152
100	3	153
\.


--
-- TOC entry 4887 (class 0 OID 24578)
-- Dependencies: 218
-- Data for Name: stations; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.stations (id, name, unix_start) FROM stdin;
1	channel1	0
2	channel2	0
3	Bob's Burgers	0
\.


--
-- TOC entry 4895 (class 0 OID 24712)
-- Dependencies: 226
-- Data for Name: tags; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.tags (id, name) FROM stdin;
1	christmas
2	thanksgiving
3	halloween
4	commercial
\.


--
-- TOC entry 4903 (class 0 OID 24866)
-- Dependencies: 234
-- Data for Name: title_metadata; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.title_metadata (id, title_id, metadata_type_id, value) FROM stdin;
\.


--
-- TOC entry 4891 (class 0 OID 24686)
-- Dependencies: 222
-- Data for Name: titles; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.titles (id, name, description) FROM stdin;
1	Bob's Burgers	Bob has a burger!!!!
0	N/A	N/A
\.


--
-- TOC entry 4901 (class 0 OID 24778)
-- Dependencies: 232
-- Data for Name: video_metadata; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.video_metadata (id, video_id, metadata_type_id, value) FROM stdin;
2	1	1	1013.0150
3	2	1	288.6635
1	1	1	565.4610
4	2	1	765.0350
5	3	1	209.7510
6	3	1	647.7930
7	3	1	879.8235
8	4	1	338.692999999999983629095
9	4	1	553.365499999999997271516
10	4	1	814.375999999999976353138
11	5	1	242.221000000000003637979
12	5	1	801.613500000000044565240
13	5	1	1092.904999999999972715159
14	6	1	316.044999999999959072738
15	6	1	649.774499999999989086064
16	6	1	976.454500000000052750693
17	7	1	341.611999999999966348696
18	7	1	751.688499999999976353138
19	7	1	1061.619999999999890860636
20	8	1	192.629999999999995452526
21	8	1	634.759000000000014551915
22	8	1	1011.779999999999972715159
23	9	1	413.600999999999999090505
24	9	1	832.185500000000047293724
25	9	1	1021.039999999999963620212
26	10	1	254.288000000000010913936
27	10	1	765.903000000000020008883
28	10	1	1042.690000000000054569682
29	11	1	267.392500000000040927262
30	11	1	620.098999999999932697392
31	11	1	1030.570000000000163709046
32	12	1	301.869000000000028194336
33	12	1	675.904500000000098225428
34	12	1	1044.840000000000145519152
35	13	1	295.274000000000000909495
36	13	1	638.451999999999998181011
37	13	1	907.491999999999961801223
38	67	1	329.41200000000003
39	67	1	550.2370000000001
40	67	1	981.7445
41	68	1	439.77250000000004
42	68	1	792.679
43	68	1	1039.6
44	69	1	317.83950000000004
45	69	1	501.6675
46	69	1	821.2155
47	70	1	348.506
48	70	1	665.394
49	70	1	993.277
50	71	1	262.774
51	71	1	711.1479999999999
52	71	1	993.9304999999999
53	72	1	358.1495
54	72	1	711.1895
55	72	1	1078.54
56	73	1	240.717
57	73	1	564.293
58	73	1	882.6524999999999
59	74	1	258.04949999999997
60	74	1	654.529
61	74	1	910.7015
62	75	1	349.376
63	75	1	687.6410000000001
64	75	1	1098.61
65	76	1	424.8325
66	76	1	730.3545
67	76	1	1014.0550000000001
68	77	1	284.75649999999996
69	77	1	629.2455
70	77	1	866.136
71	78	1	217.46699999999998
72	78	1	625.2705000000001
73	78	1	1064.63
74	79	1	191.0865
75	79	1	572.3845
76	79	1	907.581
77	80	1	340.871
78	80	1	801.342
79	80	1	1055.995
80	81	1	376.397
81	81	1	640.6400000000001
82	81	1	921.6915
83	82	1	402.6315
84	82	1	634.933
85	82	1	704.2235000000001
86	82	1	1019.54
87	83	1	287.844
88	83	1	736.7985000000001
89	83	1	1020.245
90	84	1	218.9685
91	84	1	594.302
92	84	1	953.6610000000001
93	85	1	378.837
94	85	1	768.1065
95	85	1	981.481
96	86	1	238.801
97	86	1	587.1825
98	86	1	934.9965
99	87	1	265.125
100	87	1	679.6585
101	87	1	938.8965000000001
102	88	1	243.28449999999998
103	88	1	612.2365
104	88	1	865.6614999999999
105	89	1	217.02949999999998
106	89	1	678.476
107	89	1	989.8015
108	90	1	374.916
109	90	1	636.5735
110	90	1	963.1914999999999
111	91	1	312.8125
112	91	1	774.1690000000001
113	91	1	941.2945
114	92	1	327.473
115	92	1	623.6595
116	92	1	841.987
117	93	1	350.14750000000004
118	93	1	672.4535
119	93	1	890.2850000000001
120	94	1	370.72749999999996
121	94	1	726.9665
122	94	1	937.543
123	95	1	129.9005
124	95	1	287.4955
125	95	1	698.406
126	95	1	1028.76
127	96	1	314.1055
128	96	1	642.8910000000001
129	96	1	977.6225
130	97	1	384.822
131	97	1	751.7719999999999
132	97	1	955.392
133	98	1	263.972
134	98	1	555.138
135	98	1	931.222
136	99	1	385.17650000000003
137	99	1	796.4205
138	99	1	1000.0409999999999
139	100	1	379.8675
140	100	1	775.1285
141	100	1	1089.745
142	101	1	358.102
143	101	1	771.6669999999999
144	101	1	1032.1149999999998
145	102	1	377.504
146	102	1	673.2975
147	102	1	959.4905
148	103	1	221.221
149	103	1	708.4555
150	103	1	1056.245
151	104	1	274.3365
152	104	1	750.602
153	104	1	938.135
154	105	1	256.548
155	105	1	687.6514999999999
156	105	1	957.373
157	106	1	399.0285
158	106	1	630.6925
159	106	1	691.1320000000001
160	107	1	203.2605
161	107	1	756.923
162	107	1	994.941
163	108	1	270.374
164	108	1	645.4155000000001
165	108	1	816.357
166	109	1	225.1775
167	109	1	526.3779999999999
168	109	1	954.4725000000001
169	110	1	229.209
170	110	1	590.861
171	111	1	224.558
172	111	1	684.501
173	112	1	321.554
174	112	1	768.843
175	112	1	1011.905
176	113	1	384.132
177	113	1	742.054
178	113	1	977.822
179	114	1	255.359
180	114	1	637.47
181	114	1	1069.335
182	115	1	356.947
183	115	1	674.025
184	115	1	1041.84
185	116	1	275.831
186	116	1	682.996
187	116	1	874.85
188	117	1	247.25
189	117	1	548.18
190	117	1	1026.74
191	118	1	217.113
192	118	1	572.134
193	118	1	1020.265
194	119	1	424.225
195	119	1	601.092
196	120	1	337.01
197	120	1	594.303
198	120	1	923.692
199	121	1	330.406
200	121	1	606.996
201	121	1	982.148
202	122	1	304.349
203	122	1	724.862
204	122	1	1088.4
205	123	1	248.665
206	123	1	662.462
207	123	1	954.912
208	124	1	327.841
209	124	1	777.604
210	124	1	1025.54
211	125	1	258.967
212	125	1	436.978
213	125	1	864.28
214	126	1	417.5
215	126	1	784.872
216	126	1	979.353
217	127	1	430.722
218	127	1	768.818
219	127	1	1068.03
220	128	1	389.055
221	128	1	677.844
222	128	1	1009.22
223	129	1	334.885
224	129	1	709.876
225	129	1	1025.36
226	130	1	257.424
227	130	1	811.476
228	130	1	1087
229	131	1	238.864
230	131	1	661.119
231	131	1	941.232
232	132	1	303.47
233	132	1	710.293
234	133	1	337.254
235	133	1	679.229
236	133	1	908.449
237	134	1	288.33
238	134	1	649.316
239	134	1	949.782
240	135	1	317.033
241	135	1	621.746
242	135	1	820.625
243	136	1	361.111
244	136	1	662.623
245	136	1	966.424
246	137	1	327.536
247	137	1	743.835
248	137	1	1017.35
249	138	1	414.65
250	138	1	715.465
251	138	1	1016.53
252	139	1	398.231
253	139	1	618.159
254	139	1	960.547
255	140	1	431.181
256	140	1	704.458
257	140	1	949.24
258	141	1	261.852
259	141	1	666.457
260	141	1	999.219
261	142	1	229.396
262	142	1	681.642
263	142	1	1034.115
264	143	1	360.995
265	143	1	680.908
266	143	1	1033.395
267	144	1	346.759
268	144	1	597.682
269	144	1	889.269
270	144	1	1119.155
271	145	1	280.048
272	145	1	641.906
273	145	1	915.206
274	146	1	385.552
275	146	1	760.385
276	146	1	1058.28
280	148	1	286.585
281	148	1	629.42
282	148	1	902.402
283	149	1	331.428
284	149	1	649.482
285	149	1	973.264
286	150	1	279.07
287	150	1	701.159
288	150	1	1050.63
289	151	1	339.005
290	151	1	643.186
291	151	1	884.029
292	152	1	308.12
293	152	1	666.279
294	152	1	975.016
298	154	1	297.731
299	154	1	568.255
300	154	1	961.741
301	155	1	331.206
302	155	1	687.979
303	155	1	1087.73
304	156	1	332.457
305	156	1	709.128
306	156	1	906.317
307	157	1	338.861
308	157	1	643.179
309	157	1	966.858
310	158	1	286.734
311	158	1	732.784
312	158	1	933.503
313	159	1	442.927
314	159	1	761.414
315	159	1	1040.775
316	161	1	387.976
317	161	1	672.635
318	161	1	1051.26
319	162	1	396.032
320	162	1	665.04
321	162	1	924.137
322	163	1	471.968
323	163	1	689.87
324	163	1	1013.855
325	164	1	298.76
326	164	1	718.858
327	164	1	1011.27
328	165	1	505.139
329	165	1	854.997
330	165	1	1059.145
331	166	1	401.072
332	166	1	705.797
333	166	1	1063.89
334	167	1	341.221
335	167	1	787.217
336	167	1	1047.265
337	168	1	455.381
338	168	1	836.206
339	168	1	1030.54
340	169	1	319.945
341	169	1	710.465
342	169	1	970.027
343	170	1	363.155
344	170	1	754.965
345	170	1	989.574
346	171	1	365.459
347	171	1	741.02
348	171	1	988.958
349	172	1	394.705
350	172	1	727.422
351	172	1	1030.57
352	173	1	351.026
353	173	1	660.451
354	173	1	932.684
355	174	1	264.89
356	174	1	713.796
357	174	1	1000.629
358	175	1	353.555
359	175	1	701.769
360	175	1	954.94
361	176	1	447.456
362	176	1	730.647
363	176	1	1018.75
364	177	1	320.742
365	177	1	778.361
366	177	1	1068.61
367	178	1	399.448
368	178	1	726.309
369	178	1	1036.915
370	179	1	310.435
371	179	1	705.292
372	179	1	1005.63
373	180	1	394.06
374	180	1	695.028
375	180	1	1041.135
376	181	1	464.879
377	181	1	800.197
378	181	1	1034.915
379	182	1	327.229
380	182	1	674.746
381	182	1	947.822
382	183	1	387.053
383	183	1	783.282
384	183	1	1078.08
385	184	1	436.436
386	184	1	850.021
387	184	1	1047.21
388	185	1	521.563
389	185	1	806.848
390	185	1	1033.575
391	186	1	352.269
392	186	1	721.304
393	186	1	977.56
394	187	1	370.787
395	187	1	789.209
396	187	1	986.396
397	188	1	506.673
398	188	1	863.362
399	188	1	1434.77
400	188	1	2005.215
401	188	1	2308.97
402	189	1	278.111
403	189	1	701.367
404	189	1	984.984
405	190	1	304.721
406	190	1	619.869
407	190	1	830.413
408	191	1	404.908
409	191	1	675.842
410	191	1	969.303
411	192	1	285.076
412	192	1	714.069
413	192	1	905.692
414	193	1	360.403
415	193	1	689.731
416	193	1	941.983
417	194	1	509.467
418	194	1	738.197
419	194	1	1001.125
420	195	1	356.648
421	195	1	752.377
422	195	1	991.616
423	196	1	299.216
424	196	1	743.66
425	196	1	980.563
426	197	1	426.426
427	197	1	778.111
428	197	1	952.285
429	198	1	254.963
430	198	1	723.014
431	198	1	955.037
432	199	1	439.189
433	199	1	773.523
434	199	1	926.175
435	200	1	461.169
436	200	1	847.221
437	200	1	1090.3
438	201	1	440.19
439	201	1	692.108
440	201	1	994.076
441	202	1	291.5
442	202	1	680.055
443	202	1	897.605
444	203	1	67.411
445	203	1	478.678
446	203	1	817.953
447	203	1	1282.285
448	204	1	102.035
449	204	1	102.335
450	204	1	464.581
451	204	1	866.58
452	204	1	1225.535
453	205	1	162.849
454	205	1	775.196
455	206	1	94.34
456	206	1	460.578
457	206	1	745.755
458	207	1	78.431
459	207	1	681.868
460	208	1	154.912
461	208	1	757.217
462	209	1	415.659
463	209	1	733.649
464	210	1	81.754
465	210	1	471.866
466	210	1	832.253
467	211	1	401.341
468	211	1	917.966
469	212	1	71.356
470	212	1	321.082
471	212	1	772.224
472	213	1	84.048
473	213	1	513.986
474	213	1	821.888
475	214	1	447.683
476	214	1	884.269
477	215	1	485.635
478	215	1	854.587
479	216	1	480.988
480	216	1	764.914
481	217	1	68.895
482	217	1	574.858
483	217	1	944.228
484	217	1	1280.89
485	218	1	58.22
486	218	1	621.705
\.


--
-- TOC entry 4897 (class 0 OID 24721)
-- Dependencies: 228
-- Data for Name: video_tags; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.video_tags (id, video_id, tag_id) FROM stdin;
1	14	4
2	15	4
3	16	4
4	17	4
5	18	4
6	19	4
7	20	4
8	21	4
9	22	4
10	23	4
11	24	4
12	25	4
13	26	4
14	27	4
15	28	4
16	29	4
17	30	4
18	31	4
19	32	4
20	33	4
21	34	4
22	35	4
23	36	4
24	37	4
25	38	4
26	39	4
27	40	4
28	41	4
29	42	4
30	43	4
31	44	4
32	45	4
33	46	4
34	47	4
35	48	4
36	49	4
37	50	4
38	51	4
39	52	4
40	53	4
41	54	4
42	55	4
43	56	4
44	57	4
45	58	4
46	59	4
47	60	4
48	61	4
49	62	4
50	63	4
51	64	4
52	65	4
53	66	4
\.


--
-- TOC entry 4893 (class 0 OID 24696)
-- Dependencies: 224
-- Data for Name: videos; Type: TABLE DATA; Schema: public; Owner: postgres
--

COPY public.videos (id, title_id, uri, duration) FROM stdin;
18	0	Commercials/N64/Console/Nintendo 64 It's Coming Right At Us.mp4	28.536667000000
19	0	Commercials/N64/Diddy Kong Racing/Diddy Kong Racing Meet the Fastest Monkey in the Jungle.mp4	49.851667000000
20	0	Commercials/N64/Donkey Kong 64/Donkey Kong 64 Promotional Trailer.mp4	60.720000000000
21	0	Commercials/N64/Extreme-G/Extreme-G Beta Trailer.mp4	18.970000000000
22	0	Commercials/N64/Goldeneye/Goldeneye 007 Promotional Trailer.mp4	30.510000000000
23	0	Commercials/N64/Kirby 64/U.S Kirby 64 commercial.mp4	15.650000000000
24	0	Commercials/N64/Mario Kart 64/Mario Kart 64 Commercial.mp4	30.531000000000
25	0	Commercials/N64/Paper Mario/Paper Mario Debut Commercial.mp4	29.836667000000
26	0	Commercials/N64/Perfect Dark/Perfect Dark Commercial Full.mkv	60.021000000000
27	0	Commercials/N64/Super Mario 64/Super Mario 64 Commercial.mp4	27.538333000000
28	0	Commercials/N64/Super Smash Bros/Super Smash Bros. Commercial.mp4	30.348333000000
29	0	Commercials/N64/The Legend of Zelda Majora's Mask/The Legend of Zelda Majora's Mask Trailer.mp4	60.433333000000
1	1	Bob's Burgers/1/Bob's Burgers 0101 Human Flesh.mkv	1291.164750000000
2	1	Bob's Burgers/1/Bob's Burgers 0102 Crawl Space.mkv	1291.164750000000
3	1	Bob's Burgers/1/Bob's Burgers 0103 Sacred Cow.mkv	1296.294750000000
30	0	Commercials/N64/The Legend of Zelda Ocarina of Time/The Legend of Zelda Ocarina of Time Trailer.mp4	62.368333000000
31	0	Commercials/N64/Turok 2 Seeds of Evil/Turok 2 Seeds of Evil Commercial.mp4	30.068333000000
32	0	Commercials/N64/Turok Dinosaur Hunter/Turok Dinosaur Hunter Commercial.mp4	30.741667000000
33	0	Commercials/N64/Turok Dinosaur Hunter/Turok Dinosaur Hunter German Commercial.mp4	76.160000000000
34	0	Commercials/PS1/Vigilante 8/Vigilante 8 Banned Commercial.mp4	32.067000000000
35	0	Commercials/PS1/Duke Nukem/Duke Nukem Commercial.mp4	14.883333000000
36	0	Commercials/PS1/Duke Nukem Time to Kill/Duke Nukem Time to Kill Commercial.mp4	30.091667000000
37	0	Commercials/PS1/Resident Evil 2/Resident Evil 2 Commercial 30sec.mp4	30.325267000000
38	0	Commercials/PS1/Resident Evil 2/Biohazard 2 TV Spot George Romero.mp4	31.196000000000
39	0	Commercials/PS1/Resident Evil 2/Biohazard 2 Commercial George Romero.mp4	24.960000000000
14	0	Commercials/N64/Banjo-Kazooie/Banjo-Kazooie Nintendo 64 Fruit by the Foot.mp4	30.603333000000
15	0	Commercials/N64/Banjo-Kazooie/Banjo-Kazooie Nintendo 64 Cartoon Network Giveaway.mp4	32.901667000000
16	0	Commercials/N64/Banjo-Kazooie/Banjo-Kazooie Promotional Trailer.mp4	35.641667000000
17	0	Commercials/N64/Banjo-Kazooie/Banjo-Kazooie Commercial US.mp4	29.906667000000
4	1	Bob's Burgers/1/Bob's Burgers 0104 Sexy Dance Fighting.mkv	1296.127750000000
5	1	Bob's Burgers/1/Bob's Burgers 0105 Hamburger Dinner Theater.mkv	1276.441750000000
6	1	Bob's Burgers/1/Bob's Burgers 0106 Sheesh Cab Bob.mkv	1294.125750000000
7	1	Bob's Burgers/1/Bob's Burgers 0107 Bed & Breakfast.mkv	1280.904000000000
8	1	Bob's Burgers/1/Bob's Burgers 0108 Art Crawl.mkv	1301.925750000000
9	1	Bob's Burgers/1/Bob's Burgers 0109 Spaghetti Western and Meatballs.mkv	1300.173750000000
10	1	Bob's Burgers/1/Bob's Burgers 0110 Burger War.mkv	1293.625750000000
11	1	Bob's Burgers/1/Bob's Burgers 0111 Weekend at Mort's.mkv	1276.608750000000
12	1	Bob's Burgers/1/Bob's Burgers 0112 Lobsterfest.mkv	1293.959750000000
13	1	Bob's Burgers/1/Bob's Burgers 0113 Torpedo.mkv	1294.459750000000
40	0	Commercials/PS1/Resident Evil 2/Biohazard 2 Promotional Trailer.mp4	113.151000000000
41	0	Commercials/PS1/Resident Evil 3/Resident Evil 3- Nemesis Commercial.mp4	29.866667000000
42	0	Commercials/PS2/Final Fantasy X/Final Fantasy X Promo Trailer.mp4	154.955000000000
43	0	Commercials/PS2/Grand Theft Auto III/Grand Theft Auto III Trailer.mp4	60.463333000000
44	0	Commercials/PS2/Grand Theft Auto Vice City/Grand Theft Auto Vice City Trailer 1.mp4	64.968333000000
45	0	Commercials/PS2/Grand Theft Auto Vice City/Grand Theft Auto Vice City Trailer Flock of Seagulls.mp4	60.000000000000
46	0	Commercials/PS2/Xenosaga Episode I Der Wille zur Macht/Xenosaga Episode I Der Wille zur Macht Commercial.mp4	31.695234000000
47	0	Commercials/N64/The Legend of Zelda Ocarina of Time/The Legend of Zelda Ocarina Of Time E3 Promo Trailer.mp4	56.261667000000
48	0	Commercials/Gamecube/Metroid Prime/Metroid Prime Live Action TV Spot V1.mp4	70.448333000000
49	0	Commercials/Gamecube/Pikmin/Pikmin Promo Trailer.mp4	119.605000000000
50	0	Commercials/Gamecube/Resident Evil (Remake)/Resident Evil (Remake) Commercial.mp4	32.265000000000
51	0	Commercials/Gamecube/Resident Evil (Remake)/Resident Evil (Remake) Promo.mp4	90.858333000000
52	0	Commercials/Gamecube/Resident Evil 0/Resident Evil 0 Commercial.mp4	30.208333000000
53	0	Commercials/Gamecube/Resident Evil 0/Resident Evil 0 Promo.mp4	134.071667000000
54	0	Commercials/Gamecube/Resident Evil 0/Resident Evil 0 Commercial 2.mp4	67.314633000000
55	0	Commercials/Gamecube/The Legend of Zelda The Wind Waker/The Legend of Zelda The Wind Waker With Ocarina of Time Master Quest.mp4	30.000000000000
56	0	Commercials/SNES/Donkey Kong Country/Donkey Kong Country Commercial.mp4	60.580000000000
57	0	Commercials/SNES/Super Mario Kart/Super Mario Kart Commercial.mp4	30.115000000000
58	0	Commercials/SNES/Super Mario World/Super Mario World Commercial.mp4	30.486667000000
59	0	Commercials/NES/Super Mario Bros. 2/Super Mario Bros. 2 Commercial.mp4	29.675000000000
60	0	Commercials/Gameboy/Kirby's Dream Land/Kirby's Dream Land Commercial.mp4	30.000000000000
61	0	Commercials/Dreamcast/Half-Life/Half-Life Unreleased Commercial.mp4	45.741667000000
62	0	Commercials/64 Bit Jaguar/DOOM/DOOM Commercial.mp4	34.550000000000
63	0	Commercials/NES/Konami/Konami Commercial.mp4	31.346667000000
64	0	Commercials/PS2/Final Fantasy X/Final Fantasy X Promo Trailer 2.mp4	170.225000000000
65	0	Commercials/PS2/Final Fantasy X/Final Fantasy X Promo Trailer 3.mp4	103.188333000000
66	0	Commercials/PS2/Final Fantasy X-2/Final Fantasy X-2 Promo Japanese.mp4	180.975000000000
69	0	Bob's Burgers/2/Bob's Burgers 0203 Synchronized Swimming.mkv	1272.384000000000
68	0	Bob's Burgers/2/Bob's Burgers 0202 Bob Day Afternoon.mkv	1291.488000000000
70	0	Bob's Burgers/2/Bob's Burgers 0204 Burgerboss.mkv	1295.832000000000
71	0	Bob's Burgers/2/Bob's Burgers 0205 Food Truckin'.mkv	1272.192000000000
67	0	Bob's Burgers/2/Bob's Burgers 0201 The Belchies.mkv	1287.024000000000
72	0	Bob's Burgers/2/Bob's Burgers 0206 Dr Yap.mkv	1292.136000000000
74	0	Bob's Burgers/2/Bob's Burgers 0208 Bad Tina.mkv	1290.216000000000
73	0	Bob's Burgers/2/Bob's Burgers 0207 Moody Foodie.mkv	1282.056000000000
75	0	Bob's Burgers/2/Bob's Burgers 0209 Beefsquatch.mkv	1292.808000000000
77	0	Bob's Burgers/3/Bob's Burgers 0302 Full Bars.mkv	1284.115750000000
76	0	Bob's Burgers/3/Bob's Burgers 0301 Ear-sy Rider.mkv	1292.958750000000
79	0	Bob's Burgers/3/Bob's Burgers 0304 Mutiny on the Windbreaker.mkv	1291.915750000000
78	0	Bob's Burgers/3/Bob's Burgers 0303 Bob Fires the Kids.mkv	1292.249750000000
80	0	Bob's Burgers/3/Bob's Burgers 0305 An Indecent Thanksgiving Proposal.mkv	1292.165750000000
82	0	Bob's Burgers/3/Bob's Burgers 0307 Tina-Rannosaurus Wrecks.mkv	1292.123750000000
81	0	Bob's Burgers/3/Bob's Burgers 0306 The Deepening.mkv	1271.853750000000
83	0	Bob's Burgers/3/Bob's Burgers 0308 The Unbearable Like-Likeness of Gene.mkv	1289.287750000000
84	0	Bob's Burgers/3/Bob's Burgers 0309 God Rest Ye Merry Gentle-Mannequins.mkv	1292.374750000000
85	0	Bob's Burgers/3/Bob's Burgers 0310 Mother Daughter Laser Razor.mkv	1298.046750000000
86	0	Bob's Burgers/3/Bob's Burgers 0311 Nude Beach.mkv	1297.546750000000
88	0	Bob's Burgers/3/Bob's Burgers 0313 My Fuzzy Valentine.mkv	1286.952750000000
87	0	Bob's Burgers/3/Bob's Burgers 0312 Broadcast Wagstaff School News.mkv	1282.155750000000
90	0	Bob's Burgers/3/Bob's Burgers 0315 O T The Outside Toilet.mkv	1272.145750000000
89	0	Bob's Burgers/3/Bob's Burgers 0314 Lindapendant Woman.mkv	1271.978750000000
91	0	Bob's Burgers/3/Bob's Burgers 0316 Topsy.mkv	1292.457750000000
92	0	Bob's Burgers/3/Bob's Burgers 0317 Two for Tina.mkv	1292.249750000000
95	0	Bob's Burgers/3/Bob's Burgers 0320 The Kids Run the Restaurant.mkv	1287.202750000000
93	0	Bob's Burgers/3/Bob's Burgers 0318 It Snakes a Village.mkv	1272.479750000000
94	0	Bob's Burgers/3/Bob's Burgers 0319 Family Fracas.mkv	1293.959750000000
96	0	Bob's Burgers/3/Bob's Burgers 0321 Boyz 4 Now.mkv	1272.354750000000
97	0	Bob's Burgers/3/Bob's Burgers 0322 Carpe Museum.mkv	1275.273750000000
122	0	Bob's Burgers/5/Bob's Burgers 0502 Tina and the Real Ghost.mkv	1295.044000000000
98	0	Bob's Burgers/3/Bob's Burgers 0323 The Unnatural.mkv	1292.123750000000
100	0	Bob's Burgers/4/Bob's Burgers 0402 Fort Night.mkv	1293.472000000000
99	0	Bob's Burgers/4/Bob's Burgers 0401 A River Runs Through Bob.mkv	1290.176000000000
101	0	Bob's Burgers/4/Bob's Burgers 0403 Seaplane.mkv	1292.750000000000
102	0	Bob's Burgers/4/Bob's Burgers 0404 My Big Fat Greek Bob.mkv	1282.272000000000
103	0	Bob's Burgers/4/Bob's Burgers 0405 Turkey in a Can.mkv	1272.063000000000
104	0	Bob's Burgers/4/Bob's Burgers 0406 Purple Rain-Union.mkv	1289.408000000000
105	0	Bob's Burgers/4/Bob's Burgers 0407 Bob and Deliver.mkv	1291.374000000000
106	0	Bob's Burgers/4/Bob's Burgers 0408 Christmas in the Car.mkv	1276.687000000000
107	0	Bob's Burgers/4/Bob's Burgers 0409 Slumber Party.mkv	1290.808000000000
108	0	Bob's Burgers/4/Bob's Burgers 0410 Presto Tina-o.mkv	1278.068000000000
109	0	Bob's Burgers/4/Bob's Burgers 0411 Easy Com-mercial, Easy Go-mercial.mkv	1286.368000000000
110	0	Bob's Burgers/4/Bob's Burgers 0412 The Frond Files.mkv	1295.776000000000
111	0	Bob's Burgers/4/Bob's Burgers 0413 Mazel-Tina.mkv	1289.705000000000
113	0	Bob's Burgers/4/Bob's Burgers 0415 The Kids Rob a Train.mkv	1284.576000000000
112	0	Bob's Burgers/4/Bob's Burgers 0414 Uncle Teddy.mkv	1290.247000000000
114	0	Bob's Burgers/4/Bob's Burgers 0416 I Get Psy-chic Out of You.mkv	1285.504000000000
115	0	Bob's Burgers/4/Bob's Burgers 0417 The Equestranauts.mkv	1288.704000000000
116	0	Bob's Burgers/4/Bob's Burgers 0418 Ambergris.mkv	1267.183000000000
118	0	Bob's Burgers/4/Bob's Burgers 0420 Gene It On.mkv	1285.696000000000
117	0	Bob's Burgers/4/Bob's Burgers 0419 The Kids Run Away.mkv	1286.304000000000
119	0	Bob's Burgers/4/Bob's Burgers 0421 Wharf Horse (or How Bob Saves Destroys the Town - Part I).mkv	1283.824000000000
120	0	Bob's Burgers/4/Bob's Burgers 0422 World Wharf II The Wharfening (or How Bob Saves Destroys the Town - Part II).mkv	1291.264000000000
121	0	Bob's Burgers/5/Bob's Burgers 0501 Work Hard Or Die Trying, Girl.mkv	1296.963000000000
123	0	Bob's Burgers/5/Bob's Burgers 0503 Friends With Burger-Fits.mkv	1294.836000000000
125	0	Bob's Burgers/5/Bob's Burgers 0505 Best Burger.mkv	1262.845000000000
124	0	Bob's Burgers/5/Bob's Burgers 0504 Dawn of the Peck.mkv	1281.864000000000
126	0	Bob's Burgers/5/Bob's Burgers 0506 Father of the Bob.mkv	1292.041000000000
127	0	Bob's Burgers/5/Bob's Burgers 0507 Tina Tailor Soldier Spy.mkv	1296.879000000000
128	0	Bob's Burgers/5/Bob's Burgers 0508 Midday Run.mkv	1281.948000000000
130	0	Bob's Burgers/5/Bob's Burgers 0510 Late Afternoon in the Garden of Bob and Louise.mkv	1294.000000000000
129	0	Bob's Burgers/5/Bob's Burgers 0509 Speakeasy Rider.mkv	1296.879000000000
131	0	Bob's Burgers/5/Bob's Burgers 0511 Can't Buy Me Math.mkv	1296.504000000000
132	0	Bob's Burgers/5/Bob's Burgers 0512 The Millie-Churian Candidate.mkv	1296.921000000000
133	0	Bob's Burgers/5/Bob's Burgers 0513 The Gayle Tales.mkv	1291.874000000000
134	0	Bob's Burgers/5/Bob's Burgers 0514 L'il Hard Dad.mkv	1286.828000000000
135	0	Bob's Burgers/5/Bob's Burgers 0515 Adventures In Chinchilla Sitting.mkv	1281.864000000000
136	0	Bob's Burgers/5/Bob's Burgers 0516 The Runaway Club.mkv	1291.833000000000
138	0	Bob's Burgers/5/Bob's Burgers 0518 Eat, Spray, Linda.mkv	1281.572000000000
137	0	Bob's Burgers/5/Bob's Burgers 0517 The Itty Bitty Ditty Committee.mkv	1296.963000000000
139	0	Bob's Burgers/5/Bob's Burgers 0519 Housetrap.mkv	1296.713000000000
140	0	Bob's Burgers/5/Bob's Burgers 0520 Hawk & Chick.mkv	1286.729000000000
142	0	Bob's Burgers/6/Bob's Burgers 0601 Sliding Bobs.mkv	1294.001000000000
143	0	Bob's Burgers/6/Bob's Burgers 0602 The Land Ship.mkv	1298.005000000000
141	0	Bob's Burgers/5/Bob's Burgers 0521 The Oeder Games.mkv	1296.754000000000
144	0	Bob's Burgers/6/Bob's Burgers 0603 The Hauntening.mkv	1277.109000000000
145	0	Bob's Burgers/6/Bob's Burgers 0604 Gayle Makin' Bob Sled.mkv	1291.999000000000
146	0	Bob's Burgers/6/Bob's Burgers 0605 Nice-Capades.mkv	1276.984000000000
153	0	Bob's Burgers/6/Bob's Burgers 0612 Stand by Gene.mkv	1297.012000000000
148	0	Bob's Burgers/6/Bob's Burgers 0607 The Gene & Courtney Show.mkv	1292.124000000000
149	0	Bob's Burgers/6/Bob's Burgers 0608 Sexy Dance Healing.mkv	1286.994000000000
150	0	Bob's Burgers/6/Bob's Burgers 0609 Sacred Couch.mkv	1297.046000000000
151	0	Bob's Burgers/6/Bob's Burgers 0610 Lice Things Are Lice.mkv	1297.046000000000
152	0	Bob's Burgers/6/Bob's Burgers 0611 House of 1000 Bounces.mkv	1280.967000000000
154	0	Bob's Burgers/6/Bob's Burgers 0613 Wag the Hog.mkv	1271.872000000000
155	0	Bob's Burgers/6/Bob's Burgers 0614 The Hormone-iums.mkv	1297.046000000000
156	0	Bob's Burgers/6/Bob's Burgers 0615 Pro Tiki Con Tiki.mkv	1295.975000000000
157	0	Bob's Burgers/6/Bob's Burgers 0616 Bye Bye Boo Boo.mkv	1294.983000000000
158	0	Bob's Burgers/6/Bob's Burgers 0617 The Horse Rider-er.mkv	1295.985000000000
159	0	Bob's Burgers/6/Bob's Burgers 0618 Secret Admiral-irer.mp4	1297.926000000000
160	0	Bob's Burgers/6/Bob's Burgers 0619 Glued, Where's My Bob.mp4	1293.379778000000
165	0	Bob's Burgers/7/Bob's Burgers 0705 Large Brother, Where Fart Thou.mp4	1298.560000000000
161	0	Bob's Burgers/7/Bob's Burgers 0701 Flu-ouise.mp4	1322.450556000000
167	0	Bob's Burgers/7/Bob's Burgers 0707 The Last Gingerbread House on the Left.mp4	1298.539000000000
166	0	Bob's Burgers/7/Bob's Burgers 0706 The Quirk-ducers.mp4	1293.504000000000
147	0	Bob's Burgers/6/Bob's Burgers 0606 The Cook, the Steve, the Gayle, & Her Lover.mkv	1287.047000000000
184	0	Bob's Burgers/8/Bob's Burgers 0802 The Silence of the Louise.mkv	1296.629000000000
185	0	Bob's Burgers/8/Bob's Burgers 0803 The Wolf of Wharf Street.mkv	1295.424000000000
192	0	Bob's Burgers/8/Bob's Burgers 0811 Sleeping with the Frenemy.mkv	1296.608000000000
191	0	Bob's Burgers/8/Bob's Burgers 0810 The Secret Ceramics Room of Secrets.mkv	1291.456000000000
195	0	Bob's Burgers/8/Bob's Burgers 0814 The Trouble with Doubles.mkv	1286.368000000000
199	0	Bob's Burgers/8/Bob's Burgers 0818 As I Walk Through the Alley of the Shadow of Ramps.mkv	1291.616000000000
203	0	Malcolm in the Middle/1/Malcolm in the Middle 0101 Pilot.mkv	1352.317625000000
204	0	Malcolm in the Middle/1/Malcolm in the Middle 0102 Red Dress.mkv	1330.963000000000
205	0	Malcolm in the Middle/1/Malcolm in the Middle 0103 Home Alone 4.mkv	1324.957000000000
170	0	Bob's Burgers/7/Bob's Burgers 0710 There's No Business Like Mr Business Business.mp4	1277.989378000000
174	0	Bob's Burgers/7/Bob's Burgers 0714 Aquaticism.mp4	1296.555000000000
173	0	Bob's Burgers/7/Bob's Burgers 0713 The Grand Mama-pest Hotel.mp4	1298.603000000000
175	0	Bob's Burgers/7/Bob's Burgers 0715 Ain't Miss Debatin'.mp4	1298.539000000000
177	0	Bob's Burgers/7/Bob's Burgers 0717 Zero Larp Thirty.mp4	1298.603000000000
176	0	Bob's Burgers/7/Bob's Burgers 0716 Eggs for Days.mp4	1297.600000000000
179	0	Bob's Burgers/7/Bob's Burgers 0719 Thelma & Louise Except Thelma is Linda.mp4	1298.560000000000
178	0	Bob's Burgers/7/Bob's Burgers 0718 The Laser-inth.mp4	1293.547000000000
180	0	Bob's Burgers/7/Bob's Burgers 0720 Mom, Lies, and Videotape.mp4	1283.584000000000
182	0	Bob's Burgers/7/Bob's Burgers 0722 Into the Mild.mp4	1298.042000000000
186	0	Bob's Burgers/8/Bob's Burgers 0804 Sit Me Baby One More Time.mkv	1296.544000000000
183	0	Bob's Burgers/8/Bob's Burgers 0801 Brunchsquatch.mkv	1298.592000000000
188	0	Bob's Burgers/8/Bob's Burgers 0806+0807 The Bleakening (Parts 1 & 2).mkv	2625.792000000000
190	0	Bob's Burgers/8/Bob's Burgers 0809 Y Tu Ga-Ga Tambien.mkv	1293.568000000000
189	0	Bob's Burgers/8/Bob's Burgers 0808 V for Valentine-detta.mkv	1276.448000000000
193	0	Bob's Burgers/8/Bob's Burgers 0812 The Hurt Soccer.mkv	1291.296000000000
194	0	Bob's Burgers/8/Bob's Burgers 0813 Cheer Up, Sleepy Gene.mkv	1286.592000000000
197	0	Bob's Burgers/8/Bob's Burgers 0816 Are You There Bob It's Me, Birthday.mkv	1293.632000000000
200	0	Bob's Burgers/8/Bob's Burgers 0819 Mo Mommy Mo Problems.mkv	1293.216000000000
201	0	Bob's Burgers/8/Bob's Burgers 0820 Mission Impos-slug-ble.mkv	1296.640000000000
202	0	Bob's Burgers/8/Bob's Burgers 0821 Something Old, Something New, Something Bob Caters for You.mkv	1293.216000000000
187	0	Bob's Burgers/8/Bob's Burgers 0805 Thanks-hoarding.mkv	1276.320000000000
206	0	Malcolm in the Middle/1/Malcolm in the Middle 0104 Shame.mkv	1350.449125000000
208	0	Malcolm in the Middle/1/Malcolm in the Middle 0106 Sleepover.mkv	1353.986000000000
210	0	Malcolm in the Middle/1/Malcolm in the Middle 0108 Krelboyne Picnic.mkv	1356.922250000000
207	0	Malcolm in the Middle/1/Malcolm in the Middle 0105 Malcolm Babysits.mkv	1355.921250000000
209	0	Malcolm in the Middle/1/Malcolm in the Middle 0107 Francis Escapes.mkv	1354.920250000000
211	0	Malcolm in the Middle/1/Malcolm in the Middle 0109 Lois vs. Evil.mkv	1355.921250000000
213	0	Malcolm in the Middle/1/Malcolm in the Middle 0111 Funeral.mkv	1352.918250000000
215	0	Malcolm in the Middle/1/Malcolm in the Middle 0113 Rollerskates.mkv	1345.911250000000
212	0	Malcolm in the Middle/1/Malcolm in the Middle 0110 Stock Car Races.mkv	1355.921250000000
216	0	Malcolm in the Middle/1/Malcolm in the Middle 0114 The Bots and the Bees.mkv	1354.920250000000
219	0	Malcolm in the Middle/2/Malcolm in the Middle 0201 Traffic Jam.mkv	1332.297625000000
217	0	Malcolm in the Middle/1/Malcolm in the Middle 0115 Smunday.mkv	1351.917250000000
218	0	Malcolm in the Middle/1/Malcolm in the Middle 0116 Water Park.mkv	1353.986000000000
220	0	Malcolm in the Middle/2/Malcolm in the Middle 0202 Halloween Approximately.mkv	1334.599875000000
221	0	Malcolm in the Middle/2/Malcolm in the Middle 0203 Lois's Birthday.mkv	1298.263625000000
223	0	Malcolm in the Middle/2/Malcolm in the Middle 0205 Casino.mkv	1336.301625000000
225	0	Malcolm in the Middle/2/Malcolm in the Middle 0207 Robbery.mkv	1334.800125000000
222	0	Malcolm in the Middle/2/Malcolm in the Middle 0204 Dinner Out.mkv	1337.302625000000
226	0	Malcolm in the Middle/2/Malcolm in the Middle 0208 Therapy.mkv	1337.636250000000
227	0	Malcolm in the Middle/2/Malcolm in the Middle 0209 High School Play.mkv	1336.301625000000
229	0	Malcolm in the Middle/2/Malcolm in the Middle 0211 Old Mrs. Old.mkv	1327.426125000000
228	0	Malcolm in the Middle/2/Malcolm in the Middle 0210 The Bully.mkv	1336.301625000000
230	0	Malcolm in the Middle/2/Malcolm in the Middle 0212 Krelboyne Girl.mkv	1337.302625000000
231	0	Malcolm in the Middle/2/Malcolm in the Middle 0213 New Neighbors.mkv	1335.300625000000
232	0	Malcolm in the Middle/2/Malcolm in the Middle 0214 Hal Quits.mkv	1337.302625000000
234	0	Malcolm in the Middle/2/Malcolm in the Middle 0216 Traffic Ticket.mkv	1336.501875000000
233	0	Malcolm in the Middle/2/Malcolm in the Middle 0215 The Grandparents.mkv	1334.299625000000
235	0	Malcolm in the Middle/2/Malcolm in the Middle 0217 Surgery.mkv	1338.303625000000
236	0	Malcolm in the Middle/2/Malcolm in the Middle 0218 Reese Cooks.mkv	1336.301625000000
237	0	Malcolm in the Middle/2/Malcolm in the Middle 0219 Tutoring Reese.mkv	1332.464500000000
239	0	Malcolm in the Middle/2/Malcolm in the Middle 0221 Malcolm vs. Reese.mkv	1307.853250000000
238	0	Malcolm in the Middle/2/Malcolm in the Middle 0220 Bowling.mkv	1333.165125000000
241	0	Malcolm in the Middle/2/Malcolm in the Middle 0223 Carnival.mkv	1333.098375000000
243	0	Malcolm in the Middle/2/Malcolm in the Middle 0225 Flashback.mkv	1334.499875000000
242	0	Malcolm in the Middle/2/Malcolm in the Middle 0224 Evacuation.mkv	1327.693000000000
245	0	Malcolm in the Middle/3/Malcolm in the Middle 0302 Emancipation.mkv	1328.660625000000
244	0	Malcolm in the Middle/3/Malcolm in the Middle 0301 Houseboat.mkv	1331.196500000000
246	0	Malcolm in the Middle/3/Malcolm in the Middle 0303 Book Club.mkv	1321.320000000000
248	0	Malcolm in the Middle/3/Malcolm in the Middle 0305 Charity.mkv	1328.894250000000
247	0	Malcolm in the Middle/3/Malcolm in the Middle 0304 Malcolm's Girlfriend.mkv	1314.713375000000
249	0	Malcolm in the Middle/3/Malcolm in the Middle 0306 Health Scare.mkv	1327.492875000000
254	0	Malcolm in the Middle/3/Malcolm in the Middle 0311 Company Picnic Part 1.mkv	1297.162500000000
255	0	Malcolm in the Middle/3/Malcolm in the Middle 0312 Company Picnic Part 2.mkv	1327.626250000000
251	0	Malcolm in the Middle/3/Malcolm in the Middle 0308 Poker.mkv	1328.026750000000
257	0	Malcolm in the Middle/3/Malcolm in the Middle 0314 Cynthia's Back.mkv	1326.925625000000
256	0	Malcolm in the Middle/3/Malcolm in the Middle 0313 Reese Drives.mkv	1328.226875000000
258	0	Malcolm in the Middle/3/Malcolm in the Middle 0315 Hal's Birthday.mkv	1293.558875000000
259	0	Malcolm in the Middle/3/Malcolm in the Middle 0316 Hal Coaches.mkv	1269.234625000000
260	0	Malcolm in the Middle/3/Malcolm in the Middle 0317 Dewey's Dog.mkv	1327.559625000000
261	0	Malcolm in the Middle/3/Malcolm in the Middle 0318 Poker #2.mkv	1328.060125000000
263	0	Malcolm in the Middle/3/Malcolm in the Middle 0320 Jury Duty.mkv	1326.558625000000
264	0	Malcolm in the Middle/3/Malcolm in the Middle 0321 Cliques.mkv	1328.060125000000
265	0	Malcolm in the Middle/3/Malcolm in the Middle 0322 Monkey.mkv	1327.059125000000
268	0	Malcolm in the Middle/4/Malcolm in the Middle 0403 Family Reunion.mkv	1314.212875000000
266	0	Malcolm in the Middle/4/Malcolm in the Middle 0401 Zoo.mkv	1314.713375000000
267	0	Malcolm in the Middle/4/Malcolm in the Middle 0402 Humilithon.mkv	1314.613250000000
269	0	Malcolm in the Middle/4/Malcolm in the Middle 0404 Stupid Girl.mkv	1314.813500000000
270	0	Malcolm in the Middle/4/Malcolm in the Middle 0405 Forwards Backwards.mkv	1315.647625000000
273	0	Malcolm in the Middle/4/Malcolm in the Middle 0408 Boys at the Ranch.mkv	1314.313000000000
272	0	Malcolm in the Middle/4/Malcolm in the Middle 0407 Malcolm Holds His Tongue.mkv	1315.981375000000
271	0	Malcolm in the Middle/4/Malcolm in the Middle 0406 Forbidden Girlfriend.mkv	1314.479875000000
274	0	Malcolm in the Middle/4/Malcolm in the Middle 0409 Grandma Sues.mkv	1282.848250000000
275	0	Malcolm in the Middle/4/Malcolm in the Middle 0410 If Boys Were Girls.mkv	1316.148125000000
277	0	Malcolm in the Middle/4/Malcolm in the Middle 0412 Kicked Out.mkv	1315.480875000000
276	0	Malcolm in the Middle/4/Malcolm in the Middle 0411 Long Drive.mkv	1316.815500000000
278	0	Malcolm in the Middle/4/Malcolm in the Middle 0413 Stereo Store.mkv	1315.480875000000
281	0	Malcolm in the Middle/4/Malcolm in the Middle 0416 Academic Octathlon.mkv	1316.481875000000
283	0	Malcolm in the Middle/4/Malcolm in the Middle 0418 Reese's Party.mkv	1317.149125000000
282	0	Malcolm in the Middle/4/Malcolm in the Middle 0417 Clip Show #2.mkv	1316.815500000000
285	0	Malcolm in the Middle/4/Malcolm in the Middle 0420 Baby Part 1.mkv	1352.184125000000
284	0	Malcolm in the Middle/4/Malcolm in the Middle 0419 Future Malcolm.mkv	1316.815500000000
286	0	Malcolm in the Middle/4/Malcolm in the Middle 0421 Baby Part 2.mkv	1326.325000000000
287	0	Malcolm in the Middle/4/Malcolm in the Middle 0422 Day Care.mkv	1289.354750000000
289	0	Malcolm in the Middle/5/Malcolm in the Middle 0502 Watching the Baby.mkv	1316.782125000000
288	0	Malcolm in the Middle/5/Malcolm in the Middle 0501 Vegas.mkv	1348.413750000000
290	0	Malcolm in the Middle/5/Malcolm in the Middle 0503 Goodbye Kitty.mkv	1316.448500000000
291	0	Malcolm in the Middle/5/Malcolm in the Middle 0504 Thanksgiving.mkv	1321.453500000000
292	0	Malcolm in the Middle/5/Malcolm in the Middle 0505 Malcolm Films Reese.mkv	1299.765125000000
295	0	Malcolm in the Middle/5/Malcolm in the Middle 0508 Block Party.mkv	1318.450500000000
293	0	Malcolm in the Middle/5/Malcolm in the Middle 0506 Malcolm's Job.mkv	1293.425500000000
299	0	Malcolm in the Middle/5/Malcolm in the Middle 0512 Softball.mkv	1315.447500000000
297	0	Malcolm in the Middle/5/Malcolm in the Middle 0510 Hot Tub.mkv	1316.448500000000
296	0	Malcolm in the Middle/5/Malcolm in the Middle 0509 Dirty Magazine.mkv	1316.949000000000
300	0	Malcolm in the Middle/5/Malcolm in the Middle 0513 Lois's Sister.mkv	1317.449500000000
298	0	Malcolm in the Middle/5/Malcolm in the Middle 0511 Ida's Boyfriend.mkv	1313.445500000000
301	0	Malcolm in the Middle/5/Malcolm in the Middle 0514 Malcolm Dates a Family.mkv	1318.450500000000
303	0	Malcolm in the Middle/5/Malcolm in the Middle 0516 Malcolm Visits College.mkv	1318.450500000000
302	0	Malcolm in the Middle/5/Malcolm in the Middle 0515 Reese's Apartment.mkv	1317.282625000000
304	0	Malcolm in the Middle/5/Malcolm in the Middle 0517 Polly in the Middle.mkv	1316.281625000000
305	0	Malcolm in the Middle/5/Malcolm in the Middle 0518 Dewey's Special Class.mkv	1316.448500000000
306	0	Malcolm in the Middle/5/Malcolm in the Middle 0519 Experiment.mkv	1305.437500000000
307	0	Malcolm in the Middle/5/Malcolm in the Middle 0520 Victor's Other Family.mkv	1346.645250000000
308	0	Malcolm in the Middle/5/Malcolm in the Middle 0521 Reese Joins the Army Part 1.mkv	1316.949000000000
163	0	Bob's Burgers/7/Bob's Burgers 0703 Teen-a Witch.mp4	1298.560000000000
164	0	Bob's Burgers/7/Bob's Burgers 0704 They Serve Horses, Don't They.mp4	1295.044000000000
162	0	Bob's Burgers/7/Bob's Burgers 0702 Sea Me Now.mp4	1293.568000000000
168	0	Bob's Burgers/7/Bob's Burgers 0708 Ex MachTina.mp4	1298.539000000000
169	0	Bob's Burgers/7/Bob's Burgers 0709 Bob Actually.mp4	1298.426500000000
172	0	Bob's Burgers/7/Bob's Burgers 0712 Like Gene for Chocolate.mp4	1296.939000000000
171	0	Bob's Burgers/7/Bob's Burgers 0711 A Few 'Gurt Men.mp4	1298.475000000000
181	0	Bob's Burgers/7/Bob's Burgers 0721 Paraders of the Lost Float.mp4	1278.027000000000
198	0	Bob's Burgers/8/Bob's Burgers 0817 Boywatch.mkv	1295.456000000000
196	0	Bob's Burgers/8/Bob's Burgers 0815 Go Tina on the Mountain.mkv	1296.576000000000
214	0	Malcolm in the Middle/1/Malcolm in the Middle 0112 Cheerleader.mkv	1355.921250000000
224	0	Malcolm in the Middle/2/Malcolm in the Middle 0206 Convention.mkv	1336.301625000000
240	0	Malcolm in the Middle/2/Malcolm in the Middle 0222 Mini-Bike.mkv	1334.466500000000
250	0	Malcolm in the Middle/3/Malcolm in the Middle 0307 Christmas.mkv	1329.094375000000
252	0	Malcolm in the Middle/3/Malcolm in the Middle 0309 Reese's Job.mkv	1326.525250000000
253	0	Malcolm in the Middle/3/Malcolm in the Middle 0310 Lois's Makeover.mkv	1327.292625000000
262	0	Malcolm in the Middle/3/Malcolm in the Middle 0319 Clip Show.mkv	1329.328000000000
280	0	Malcolm in the Middle/4/Malcolm in the Middle 0415 Garage Sale.mkv	1317.482875000000
279	0	Malcolm in the Middle/4/Malcolm in the Middle 0414 Hal's Friend.mkv	1316.815500000000
294	0	Malcolm in the Middle/5/Malcolm in the Middle 0507 Christmas Trees.mkv	1303.435500000000
310	0	Malcolm in the Middle/6/Malcolm in the Middle 0601 Reese Comes Home.mkv	1303.435500000000
309	0	Malcolm in the Middle/5/Malcolm in the Middle 0522 Reese Joins the Army Part 2.mkv	1317.449500000000
311	0	Malcolm in the Middle/6/Malcolm in the Middle 0602 Buseys Run Away.mkv	1317.449500000000
312	0	Malcolm in the Middle/6/Malcolm in the Middle 0603 Standee.mkv	1318.450500000000
313	0	Malcolm in the Middle/6/Malcolm in the Middle 0604 Pearl Harbor.mkv	1345.410750000000
314	0	Malcolm in the Middle/6/Malcolm in the Middle 0605 Kitty's Back.mkv	1316.448500000000
315	0	Malcolm in the Middle/6/Malcolm in the Middle 0606 Hal's Christmas Gift.mkv	1316.448500000000
316	0	Malcolm in the Middle/6/Malcolm in the Middle 0607 Hal Sleepwalks.mkv	1317.449500000000
319	0	Malcolm in the Middle/6/Malcolm in the Middle 0610 Billboard.mkv	1318.450500000000
320	0	Malcolm in the Middle/6/Malcolm in the Middle 0611 Dewey's Opera.mkv	1344.409750000000
318	0	Malcolm in the Middle/6/Malcolm in the Middle 0609 Malcolm's Car.mkv	1315.447500000000
322	0	Malcolm in the Middle/6/Malcolm in the Middle 0613 Tiki Lounge.mkv	1315.447500000000
324	0	Malcolm in the Middle/6/Malcolm in the Middle 0615 Chad's Sleepover.mkv	1316.448500000000
323	0	Malcolm in the Middle/6/Malcolm in the Middle 0614 Ida Loses a Leg.mkv	1318.450500000000
325	0	Malcolm in the Middle/6/Malcolm in the Middle 0616 No Motorcycles.mkv	1316.448500000000
326	0	Malcolm in the Middle/6/Malcolm in the Middle 0617 Butterflies.mkv	1309.441500000000
327	0	Malcolm in the Middle/6/Malcolm in the Middle 0618 Ida's Dance.mkv	1318.450500000000
329	0	Malcolm in the Middle/6/Malcolm in the Middle 0620 Stilts.mkv	1367.332625000000
328	0	Malcolm in the Middle/6/Malcolm in the Middle 0619 Motivational Speaker.mkv	1316.448500000000
330	0	Malcolm in the Middle/6/Malcolm in the Middle 0621 Buseys Take a Hostage.mkv	1317.449500000000
332	0	Malcolm in the Middle/7/Malcolm in the Middle 0701 Burning Man.mkv	1317.716375000000
333	0	Malcolm in the Middle/7/Malcolm in the Middle 0702 Health Insurance.mkv	1296.695375000000
331	0	Malcolm in the Middle/6/Malcolm in the Middle 0622 Mrs. Tri-County.mkv	1348.413750000000
335	0	Malcolm in the Middle/7/Malcolm in the Middle 0704 Halloween.mkv	1321.720375000000
336	0	Malcolm in the Middle/7/Malcolm in the Middle 0705 Jessica Stays Over.mkv	1322.721375000000
338	0	Malcolm in the Middle/7/Malcolm in the Middle 0707 Blackout.mkv	1352.751375000000
337	0	Malcolm in the Middle/7/Malcolm in the Middle 0706 Secret Boyfriend.mkv	1322.721375000000
339	0	Malcolm in the Middle/7/Malcolm in the Middle 0708 Army Buddy.mkv	1321.720375000000
340	0	Malcolm in the Middle/7/Malcolm in the Middle 0709 Malcolm Defends Reese.mkv	1323.722375000000
341	0	Malcolm in the Middle/7/Malcolm in the Middle 0710 Malcolm's Money.mkv	1315.714375000000
344	0	Malcolm in the Middle/7/Malcolm in the Middle 0713 Mono.mkv	1323.722375000000
345	0	Malcolm in the Middle/7/Malcolm in the Middle 0714 Hal Grieves.mkv	1352.751375000000
343	0	Malcolm in the Middle/7/Malcolm in the Middle 0712 College Recruiters.mkv	1321.720375000000
342	0	Malcolm in the Middle/7/Malcolm in the Middle 0711 Bride of Ida.mkv	1351.750375000000
346	0	Malcolm in the Middle/7/Malcolm in the Middle 0715 A.A..mkv	1320.719375000000
350	0	Malcolm in the Middle/7/Malcolm in the Middle 0719 Stevie in the Hospital.mkv	1324.723375000000
348	0	Malcolm in the Middle/7/Malcolm in the Middle 0717 Hal's Dentist.mkv	1288.687375000000
347	0	Malcolm in the Middle/7/Malcolm in the Middle 0716 Lois Strikes Back.mkv	1321.720375000000
349	0	Malcolm in the Middle/7/Malcolm in the Middle 0718 Bomb Shelter.mkv	1324.723375000000
351	0	Malcolm in the Middle/7/Malcolm in the Middle 0720 Cattle Court.mkv	1322.721375000000
352	0	Malcolm in the Middle/7/Malcolm in the Middle 0721 Morp.mkv	1323.722375000000
353	0	Malcolm in the Middle/7/Malcolm in the Middle 0722 Graduation.mkv	1354.753375000000
317	0	Malcolm in the Middle/6/Malcolm in the Middle 0608 Lois Battles Jamie.mkv	1315.447500000000
321	0	Malcolm in the Middle/6/Malcolm in the Middle 0612 Living Will.mkv	1317.449500000000
334	0	Malcolm in the Middle/7/Malcolm in the Middle 0703 Reese vs. Stevie.mkv	1321.720375000000
\.


--
-- TOC entry 4919 (class 0 OID 0)
-- Dependencies: 229
-- Name: metadata_types_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.metadata_types_id_seq', 1, true);


--
-- TOC entry 4920 (class 0 OID 0)
-- Dependencies: 219
-- Name: segments_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.segments_id_seq', 25, false);


--
-- TOC entry 4921 (class 0 OID 0)
-- Dependencies: 236
-- Name: station_videos_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.station_videos_id_seq', 149, true);


--
-- TOC entry 4922 (class 0 OID 0)
-- Dependencies: 217
-- Name: stations_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.stations_id_seq', 3, true);


--
-- TOC entry 4923 (class 0 OID 0)
-- Dependencies: 225
-- Name: tags_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.tags_id_seq', 4, true);


--
-- TOC entry 4924 (class 0 OID 0)
-- Dependencies: 233
-- Name: title_metadata_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.title_metadata_id_seq', 15, true);


--
-- TOC entry 4925 (class 0 OID 0)
-- Dependencies: 221
-- Name: titles_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.titles_id_seq', 1, true);


--
-- TOC entry 4926 (class 0 OID 0)
-- Dependencies: 231
-- Name: video_metadata_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.video_metadata_id_seq', 486, true);


--
-- TOC entry 4927 (class 0 OID 0)
-- Dependencies: 227
-- Name: video_tags_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.video_tags_id_seq', 53, true);


--
-- TOC entry 4928 (class 0 OID 0)
-- Dependencies: 223
-- Name: videos_id_seq; Type: SEQUENCE SET; Schema: public; Owner: postgres
--

SELECT pg_catalog.setval('public.videos_id_seq', 353, false);


--
-- TOC entry 4720 (class 2606 OID 24776)
-- Name: metadata_types metadata_types_name_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.metadata_types
    ADD CONSTRAINT metadata_types_name_key UNIQUE (name);


--
-- TOC entry 4722 (class 2606 OID 24774)
-- Name: metadata_types metadata_types_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.metadata_types
    ADD CONSTRAINT metadata_types_pkey PRIMARY KEY (id);


--
-- TOC entry 4702 (class 2606 OID 24811)
-- Name: segments segments_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.segments
    ADD CONSTRAINT segments_pkey PRIMARY KEY (id);


--
-- TOC entry 4732 (class 2606 OID 25121)
-- Name: station_videos station_videos_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.station_videos
    ADD CONSTRAINT station_videos_pkey PRIMARY KEY (id);


--
-- TOC entry 4697 (class 2606 OID 24585)
-- Name: stations stations_name_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.stations
    ADD CONSTRAINT stations_name_key UNIQUE (name);


--
-- TOC entry 4699 (class 2606 OID 24635)
-- Name: stations stations_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.stations
    ADD CONSTRAINT stations_pkey PRIMARY KEY (id);


--
-- TOC entry 4711 (class 2606 OID 24719)
-- Name: tags tags_name_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_name_key UNIQUE (name);


--
-- TOC entry 4713 (class 2606 OID 24717)
-- Name: tags tags_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_pkey PRIMARY KEY (id);


--
-- TOC entry 4730 (class 2606 OID 24872)
-- Name: title_metadata title_metadata_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.title_metadata
    ADD CONSTRAINT title_metadata_pkey PRIMARY KEY (id);


--
-- TOC entry 4704 (class 2606 OID 24864)
-- Name: titles titles_name_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.titles
    ADD CONSTRAINT titles_name_key UNIQUE (name);


--
-- TOC entry 4706 (class 2606 OID 24694)
-- Name: titles titles_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.titles
    ADD CONSTRAINT titles_pkey PRIMARY KEY (id);


--
-- TOC entry 4726 (class 2606 OID 24785)
-- Name: video_metadata video_metadata_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_metadata
    ADD CONSTRAINT video_metadata_pkey PRIMARY KEY (id);


--
-- TOC entry 4716 (class 2606 OID 24726)
-- Name: video_tags video_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_tags
    ADD CONSTRAINT video_tags_pkey PRIMARY KEY (id);


--
-- TOC entry 4718 (class 2606 OID 24728)
-- Name: video_tags video_tags_video_id_tag_id_key; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_tags
    ADD CONSTRAINT video_tags_video_id_tag_id_key UNIQUE (video_id, tag_id);


--
-- TOC entry 4709 (class 2606 OID 24705)
-- Name: videos videos_pkey; Type: CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.videos
    ADD CONSTRAINT videos_pkey PRIMARY KEY (id);


--
-- TOC entry 4700 (class 1259 OID 24598)
-- Name: idx_segments_station_order; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_segments_station_order ON public.segments USING btree (station_id, order_num);


--
-- TOC entry 4727 (class 1259 OID 24883)
-- Name: idx_title_metadata_title_id; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_title_metadata_title_id ON public.title_metadata USING btree (title_id);


--
-- TOC entry 4728 (class 1259 OID 24884)
-- Name: idx_title_metadata_value_gin; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_title_metadata_value_gin ON public.title_metadata USING gin (value);


--
-- TOC entry 4723 (class 1259 OID 24802)
-- Name: idx_video_metadata_value_gin; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_video_metadata_value_gin ON public.video_metadata USING gin (value);


--
-- TOC entry 4724 (class 1259 OID 24801)
-- Name: idx_video_metadata_video_id; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_video_metadata_video_id ON public.video_metadata USING btree (video_id);


--
-- TOC entry 4714 (class 1259 OID 24797)
-- Name: idx_video_tags_video_id; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_video_tags_video_id ON public.video_tags USING btree (video_id);


--
-- TOC entry 4707 (class 1259 OID 24796)
-- Name: idx_videos_title_id; Type: INDEX; Schema: public; Owner: postgres
--

CREATE INDEX idx_videos_title_id ON public.videos USING btree (title_id);


--
-- TOC entry 4733 (class 2606 OID 24636)
-- Name: segments segments_station_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.segments
    ADD CONSTRAINT segments_station_id_fkey FOREIGN KEY (station_id) REFERENCES public.stations(id) ON DELETE CASCADE;


--
-- TOC entry 4739 (class 2606 OID 24878)
-- Name: title_metadata title_metadata_metadata_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.title_metadata
    ADD CONSTRAINT title_metadata_metadata_type_id_fkey FOREIGN KEY (metadata_type_id) REFERENCES public.metadata_types(id) ON DELETE CASCADE;


--
-- TOC entry 4740 (class 2606 OID 24873)
-- Name: title_metadata title_metadata_title_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.title_metadata
    ADD CONSTRAINT title_metadata_title_id_fkey FOREIGN KEY (title_id) REFERENCES public.titles(id) ON DELETE CASCADE;


--
-- TOC entry 4737 (class 2606 OID 24791)
-- Name: video_metadata video_metadata_metadata_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_metadata
    ADD CONSTRAINT video_metadata_metadata_type_id_fkey FOREIGN KEY (metadata_type_id) REFERENCES public.metadata_types(id) ON DELETE CASCADE;


--
-- TOC entry 4738 (class 2606 OID 24786)
-- Name: video_metadata video_metadata_video_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_metadata
    ADD CONSTRAINT video_metadata_video_id_fkey FOREIGN KEY (video_id) REFERENCES public.videos(id) ON DELETE CASCADE;


--
-- TOC entry 4735 (class 2606 OID 24734)
-- Name: video_tags video_tags_tag_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_tags
    ADD CONSTRAINT video_tags_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES public.tags(id) ON DELETE CASCADE;


--
-- TOC entry 4736 (class 2606 OID 24729)
-- Name: video_tags video_tags_video_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.video_tags
    ADD CONSTRAINT video_tags_video_id_fkey FOREIGN KEY (video_id) REFERENCES public.videos(id) ON DELETE CASCADE;


--
-- TOC entry 4734 (class 2606 OID 24706)
-- Name: videos videos_title_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: postgres
--

ALTER TABLE ONLY public.videos
    ADD CONSTRAINT videos_title_id_fkey FOREIGN KEY (title_id) REFERENCES public.titles(id) ON DELETE CASCADE;


-- Completed on 2025-09-29 23:20:52

--
-- PostgreSQL database dump complete
--

\unrestrict fhhChmVPkfpO10dHQRM6x5ayUpXr2YY9wKj2tdohAXjdLOF13Lpg2VGMxp7JU8H

