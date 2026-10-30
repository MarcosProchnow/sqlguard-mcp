-- Banco de demonstração: uma loja online fictícia. Nenhum dado real.
CREATE TABLE clientes (
  id        INTEGER PRIMARY KEY,
  nome      TEXT NOT NULL,
  cidade    TEXT NOT NULL,
  criado_em TEXT NOT NULL
);

CREATE TABLE produtos (
  id        INTEGER PRIMARY KEY,
  nome      TEXT NOT NULL,
  categoria TEXT NOT NULL,
  preco     REAL NOT NULL
);

CREATE TABLE pedidos (
  id         INTEGER PRIMARY KEY,
  cliente_id INTEGER NOT NULL REFERENCES clientes(id),
  data       TEXT NOT NULL,
  status     TEXT NOT NULL
);

CREATE TABLE itens_pedido (
  pedido_id  INTEGER NOT NULL REFERENCES pedidos(id),
  produto_id INTEGER NOT NULL REFERENCES produtos(id),
  quantidade INTEGER NOT NULL,
  preco_unit REAL NOT NULL,
  PRIMARY KEY (pedido_id, produto_id)
);

INSERT INTO clientes VALUES
  (1, 'Ana Souza',      'Manaus',        '2025-02-10'),
  (2, 'Bruno Lima',     'Boa Vista',     '2025-03-05'),
  (3, 'Carla Mendes',   'Belém',         '2025-03-21'),
  (4, 'Diego Alves',    'Manaus',        '2025-05-02'),
  (5, 'Elisa Rocha',    'Porto Velho',   '2025-06-14'),
  (6, 'Fábio Nunes',    'Boa Vista',     '2025-08-30');

INSERT INTO produtos VALUES
  (1, 'Teclado mecânico',   'periféricos', 289.90),
  (2, 'Mouse sem fio',      'periféricos',  99.90),
  (3, 'Monitor 24"',        'monitores',   849.00),
  (4, 'Webcam HD',          'periféricos', 179.00),
  (5, 'Headset',            'áudio',       229.50),
  (6, 'Cabo HDMI 2m',       'cabos',        29.90),
  (7, 'Hub USB-C',          'cabos',       149.00),
  (8, 'Suporte de notebook','acessórios',   89.90);

INSERT INTO pedidos VALUES
  (1,  1, '2026-01-08', 'pago'),
  (2,  1, '2026-02-15', 'pago'),
  (3,  2, '2026-02-20', 'pago'),
  (4,  3, '2026-03-02', 'cancelado'),
  (5,  3, '2026-03-18', 'pago'),
  (6,  4, '2026-04-07', 'pago'),
  (7,  5, '2026-04-22', 'pendente'),
  (8,  2, '2026-05-11', 'pago'),
  (9,  6, '2026-06-03', 'pago'),
  (10, 1, '2026-07-19', 'pago'),
  (11, 4, '2026-08-25', 'pendente'),
  (12, 5, '2026-09-30', 'pago');

INSERT INTO itens_pedido VALUES
  (1, 1, 1, 289.90), (1, 2, 2,  99.90),
  (2, 3, 1, 849.00),
  (3, 5, 1, 229.50), (3, 6, 3,  29.90),
  (4, 4, 1, 179.00),
  (5, 3, 1, 849.00), (5, 7, 1, 149.00),
  (6, 1, 1, 289.90), (6, 8, 1,  89.90),
  (7, 2, 1,  99.90),
  (8, 4, 2, 179.00), (8, 5, 1, 229.50),
  (9, 3, 1, 849.00), (9, 6, 2,  29.90),
  (10, 7, 2, 149.00), (10, 1, 1, 289.90),
  (11, 8, 1,  89.90),
  (12, 5, 1, 229.50), (12, 2, 1,  99.90);
