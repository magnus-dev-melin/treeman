-- Seed dump loaded into the source DB before migrations run.
-- mysqldump-style: USE statement is stripped by the loader; we land
-- in whatever DB the connection is scoped to (the per-worktree
-- name_template-rendered DB).
CREATE TABLE products (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(255) NOT NULL,
  price DECIMAL(10, 2) NOT NULL
) ENGINE=InnoDB;

-- Audit table + trigger: clones must replay triggers, not just copy
-- tables/tablespaces (issue #20).
CREATE TABLE product_audit (
  id INT PRIMARY KEY AUTO_INCREMENT,
  product_id INT NOT NULL
) ENGINE=InnoDB;

CREATE TRIGGER products_ai AFTER INSERT ON products
FOR EACH ROW INSERT INTO product_audit (product_id) VALUES (NEW.id);

-- Views: clones must replay them too, reading the clone's own tables
-- (issue #144). a_cheap reads z_priced, so alphabetical order is the
-- wrong creation order.
CREATE VIEW z_priced AS SELECT id, name, price FROM products;
CREATE VIEW a_cheap AS SELECT id, name FROM z_priced WHERE price < 20;

INSERT INTO products (name, price) VALUES
  ('Widget', 9.99),
  ('Gadget', 19.99),
  ('Doohickey', 29.99);
