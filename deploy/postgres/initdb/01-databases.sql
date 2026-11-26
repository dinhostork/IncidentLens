-- One database and role per demo service. Runs once, when the data volume is
-- created. Tables are created by the services themselves on startup.
CREATE ROLE catalog LOGIN PASSWORD 'catalog';
CREATE DATABASE catalog OWNER catalog;

CREATE ROLE orders LOGIN PASSWORD 'orders';
CREATE DATABASE orders OWNER orders;

CREATE ROLE payments LOGIN PASSWORD 'payments';
CREATE DATABASE payments OWNER payments;
